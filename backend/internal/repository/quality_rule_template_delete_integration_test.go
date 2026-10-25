//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func qualityTemplateDeleteFixture(t *testing.T) (*service.ScheduledTestService, *qualityBPSFixture, *service.QualityRuleTemplate, int64) {
	t.Helper()
	ctx := context.Background()
	var plainAccount int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable)
		VALUES('template-delete-control','openai','oauth','active',true) RETURNING id`).Scan(&plainAccount))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduled_test_plans WHERE account_id=$1`, plainAccount)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, plainAccount)
	})
	plans := NewScheduledTestPlanRepository(integrationDB)
	plain, err := service.NewScheduledTestService(plans, nil).CreatePlan(ctx, &service.ScheduledTestPlan{
		AccountID: plainAccount, ModelID: "sol", CronExpression: "*/30 * * * *", MaxResults: 100,
	})
	require.NoError(t, err)
	f, _, _ := newQualityIsolationFixture(t)
	// Both accounts share the template's policy, but only the second has failed.
	// The unisolated child is deleted first, exercising rollback of earlier work.
	plain.PelicanConfig = f.plan.PelicanConfig
	_, err = plans.Update(ctx, plain)
	require.NoError(t, err)
	require.Less(t, plain.ID, f.plan.ID)
	templates := NewQualityRuleTemplateRepository(integrationEntClient, integrationDB)
	template, err := templates.Create(ctx, &service.QualityRuleTemplate{
		ModelID: "sol", CronExpression: "*/30 * * * *", MaxResults: 100, PelicanConfig: f.plan.PelicanConfig,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM quality_rule_templates WHERE id=$1`, template.ID)
	})
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO quality_rule_template_accounts(template_id,account_id,plan_id)
		VALUES($1,$2,$3),($1,$4,$5)`, template.ID, plainAccount, plain.ID, f.account, f.plan.ID)
	require.NoError(t, err)
	svc := service.ProvideScheduledTestService(plans, NewScheduledTestResultRepository(integrationDB), templates, nil)
	return svc, f, template, plain.ID
}

func qualityTemplateDeleteCounts(t *testing.T, templateID, plainID, isolatedID int64) [4]int {
	t.Helper()
	var counts [4]int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM quality_rule_templates WHERE id=$1),
		(SELECT count(*) FROM scheduled_test_plans WHERE id=$2),
		(SELECT count(*) FROM scheduled_test_plans WHERE id=$3),
		(SELECT count(*) FROM quality_rule_template_accounts WHERE template_id=$1)`, templateID, plainID, isolatedID).
		Scan(&counts[0], &counts[1], &counts[2], &counts[3]))
	return counts
}

func TestQualityTemplateDeleteRollsBackIsolatedChild(t *testing.T) {
	ctx := context.Background()
	svc, f, template, plainID := qualityTemplateDeleteFixture(t)
	require.Equal(t, "groups_removed", isolationRound(f, "failed", "failed"))
	before := f.extra()
	for attempt := 0; attempt < 2; attempt++ {
		deleted, err := svc.DeleteQualityTemplate(ctx, template.ID, true)
		require.Equal(t, service.QualityScopeConflictReason, infraerrors.Reason(err))
		require.Zero(t, deleted, "the transaction did not commit any child deletion")
		require.Equal(t, [4]int{1, 1, 1, 2}, qualityTemplateDeleteCounts(t, template.ID, plainID, f.plan.ID))
		require.Equal(t, before, f.extra(), "failed deletion preserves account restrictions")
	}
	// Recovering the rule makes the same template deletion safe to retry.
	require.Equal(t, "restored", isolationRound(f, "passed", "passed"))
	deleted, err := svc.DeleteQualityTemplate(ctx, template.ID, true)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	require.Equal(t, [4]int{}, qualityTemplateDeleteCounts(t, template.ID, plainID, f.plan.ID))
}

func TestQualityTemplateDeleteKeepsIsolatedChildrenWhenRequested(t *testing.T) {
	svc, f, template, plainID := qualityTemplateDeleteFixture(t)
	require.Equal(t, "groups_removed", isolationRound(f, "failed", "failed"))
	before := f.extra()
	deleted, err := svc.DeleteQualityTemplate(context.Background(), template.ID, false)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.Equal(t, [4]int{0, 1, 1, 0}, qualityTemplateDeleteCounts(t, template.ID, plainID, f.plan.ID))
	require.Equal(t, before, f.extra())
}

func TestQualityTemplateDeleteDatabaseFailureRollsBackChildren(t *testing.T) {
	ctx := context.Background()
	svc, f, template, plainID := qualityTemplateDeleteFixture(t)
	name := "template_delete_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF OLD.id=%d THEN RAISE EXCEPTION 'injected template delete failure'; END IF; RETURN OLD; END; $$;
		CREATE TRIGGER %s BEFORE DELETE ON quality_rule_templates FOR EACH ROW EXECUTE FUNCTION %s()`, name, template.ID, name, name))
	require.NoError(t, err)
	cleanup := func() {
		_, _ = integrationDB.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON quality_rule_templates; DROP FUNCTION IF EXISTS %s()", name, name))
	}
	t.Cleanup(cleanup)
	deleted, err := svc.DeleteQualityTemplate(ctx, template.ID, true)
	require.ErrorContains(t, err, "injected template delete failure")
	require.Zero(t, deleted)
	require.Equal(t, [4]int{1, 1, 1, 2}, qualityTemplateDeleteCounts(t, template.ID, plainID, f.plan.ID))
	cleanup()
	deleted, err = svc.DeleteQualityTemplate(ctx, template.ID, true)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	require.Equal(t, [4]int{}, qualityTemplateDeleteCounts(t, template.ID, plainID, f.plan.ID))
}
