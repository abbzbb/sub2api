package service

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

type openAICodexHarvestProxy struct {
	URL    string
	ExitIP string
	Name   string
}

const openAICodexWarpHarvestCacheTTL = 5 * time.Second

type cachedOpenAICodexWarpHarvest struct {
	candidates []openAICodexHarvestProxy
	fetchedAt  time.Time
}

func (s *OpenAIGatewayService) SetWarpGatewayClient(c *WarpGatewayClient) {
	if s == nil {
		return
	}
	s.warpGateway = c
	s.openaiCodexWarpOnce.Do(func() {})
}

func (s *OpenAIGatewayService) warpGatewayForHarvest() *WarpGatewayClient {
	if s == nil {
		return nil
	}
	if s.warpGateway != nil {
		return s.warpGateway
	}
	if s.cfg == nil || !s.cfg.Warp.Enabled {
		return nil
	}
	s.openaiCodexWarpOnce.Do(func() {
		c, err := ProvideWarpGatewayClient(s.cfg)
		if err != nil {
			logger.L().Warn("openai_codex_ticket warp client init failed", zap.Error(err))
			return
		}
		if c != nil && c.Enabled() {
			s.warpGateway = c
		}
	})
	return s.warpGateway
}

// OpenAICodexTicketHarvestConfigured reports whether harvest can run from
// config alone (explicit proxy URL or a configured warp-gateway). It must not
// HTTP-probe the snapshot: fail_closed and account Blocked run on the
// scheduling / admin hot path.
func OpenAICodexTicketHarvestConfigured(cfg *config.Config, harvestProxyURL string) bool {
	if strings.TrimSpace(harvestProxyURL) != "" {
		return true
	}
	return cfg != nil && cfg.Warp.Enabled && strings.TrimSpace(cfg.Warp.Gateway.BaseURL) != ""
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestConfigured() bool {
	if s == nil {
		return false
	}
	if OpenAICodexTicketHarvestConfigured(s.cfg, s.openAICodexTicketHarvestProxyURL()) {
		return true
	}
	return s.warpGateway != nil && s.warpGateway.Enabled()
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestAvailable(ctx context.Context) bool {
	if s.openAICodexTicketHarvestProxyURLContext(ctx) != "" {
		return true
	}
	return len(s.listOpenAICodexTicketHarvestProxies(ctx)) > 0
}

func (s *OpenAIGatewayService) pickOpenAICodexTicketHarvestProxy(ctx context.Context) openAICodexHarvestProxy {
	pool := pickOpenAICodexHarvestProxies(s.listOpenAICodexTicketHarvestProxies(ctx))
	if len(pool) == 0 {
		return openAICodexHarvestProxy{URL: s.openAICodexTicketHarvestProxyURLContext(ctx)}
	}
	n := atomic.AddUint64(&s.openaiCodexHarvestRR, 1)
	return pool[int((n-1)%uint64(len(pool)))]
}

func (s *OpenAIGatewayService) listOpenAICodexTicketHarvestProxies(ctx context.Context) []openAICodexHarvestProxy {
	configured := s.openAICodexTicketHarvestProxyURLContext(ctx)
	client := s.warpGatewayForHarvest()
	if client == nil || !client.Enabled() {
		if configured == "" {
			return nil
		}
		return []openAICodexHarvestProxy{{URL: configured, Name: "configured"}}
	}
	now := time.Now()
	s.openaiCodexWarpHarvestMu.Lock()
	if !s.openaiCodexWarpHarvest.fetchedAt.IsZero() && now.Sub(s.openaiCodexWarpHarvest.fetchedAt) < openAICodexWarpHarvestCacheTTL {
		out := s.openaiCodexWarpHarvest.candidates
		s.openaiCodexWarpHarvestMu.Unlock()
		return out
	}
	s.openaiCodexWarpHarvestMu.Unlock()

	snapCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	snap, err := client.PoolSnapshot(snapCtx)
	var candidates []openAICodexHarvestProxy
	if err != nil {
		logger.L().Debug("openai_codex_ticket warp snapshot failed", zap.Error(err))
		if configured != "" {
			candidates = []openAICodexHarvestProxy{{URL: configured, Name: "configured"}}
		}
	} else {
		candidates = harvestProxiesFromSnapshot(snap, configured)
	}
	s.openaiCodexWarpHarvestMu.Lock()
	s.openaiCodexWarpHarvest = cachedOpenAICodexWarpHarvest{candidates: candidates, fetchedAt: time.Now()}
	s.openaiCodexWarpHarvestMu.Unlock()
	return candidates
}

func harvestProxiesFromSnapshot(snap *WarpPoolSnapshot, configured string) []openAICodexHarvestProxy {
	seen := map[string]struct{}{}
	var out []openAICodexHarvestProxy
	add := func(p openAICodexHarvestProxy) {
		url := strings.TrimSpace(p.URL)
		if url == "" {
			return
		}
		if _, ok := seen[url]; ok {
			return
		}
		seen[url] = struct{}{}
		p.URL = url
		out = append(out, p)
	}
	if snap != nil {
		for _, inst := range snap.Instances {
			status := strings.ToLower(strings.TrimSpace(inst.Status))
			if status != "" && status != "running" {
				continue
			}
			add(openAICodexHarvestProxy{
				URL:    inst.SocksURL(),
				ExitIP: strings.TrimSpace(inst.ExitIP),
				Name:   inst.Name,
			})
		}
		if len(snap.Instances) == 0 {
			for _, raw := range snap.SocksURLs {
				add(openAICodexHarvestProxy{URL: raw})
			}
		}
	}
	if configured != "" {
		add(openAICodexHarvestProxy{URL: configured, Name: "configured"})
	}
	return out
}

// pickOpenAICodexHarvestProxies prefers instances whose exit_ip is unique.
// When every running instance shares an IP, the full pool is used so harvest
// can still fail over across SOCKS ports after a rotate.
func pickOpenAICodexHarvestProxies(candidates []openAICodexHarvestProxy) []openAICodexHarvestProxy {
	if len(candidates) <= 1 {
		return candidates
	}
	byIP := map[string][]openAICodexHarvestProxy{}
	var unknown []openAICodexHarvestProxy
	for _, c := range candidates {
		ip := strings.TrimSpace(c.ExitIP)
		if ip == "" {
			unknown = append(unknown, c)
			continue
		}
		byIP[ip] = append(byIP[ip], c)
	}
	var unique []openAICodexHarvestProxy
	ips := make([]string, 0, len(byIP))
	for ip := range byIP {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	for _, ip := range ips {
		if len(byIP[ip]) == 1 {
			unique = append(unique, byIP[ip][0])
		}
	}
	if len(unique) > 0 {
		return append(unique, unknown...)
	}
	return candidates
}

func harvestProxyLogFields(p openAICodexHarvestProxy) []zap.Field {
	fields := []zap.Field{
		zap.String("harvest_proxy", harvestProxyHostPort(p.URL)),
	}
	if p.Name != "" {
		fields = append(fields, zap.String("harvest_proxy_name", p.Name))
	}
	if p.ExitIP != "" {
		fields = append(fields, zap.String("harvest_exit_ip", p.ExitIP))
	}
	return fields
}

func harvestProxyHostPort(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return MaskProxyURL(raw)
	}
	return parsed.Host
}
