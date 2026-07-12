package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomMenuItemShowsOpenInNewTab_DefaultTrue(t *testing.T) {
	raw := `{"id":"m1","label":"Help","icon_svg":"","url":"https://example.com","visibility":"user","sort_order":0}`
	var item CustomMenuItem
	require.NoError(t, json.Unmarshal([]byte(raw), &item))
	require.True(t, CustomMenuItemShowsOpenInNewTab(item))

	off := false
	item.ShowOpenInNewTab = &off
	require.False(t, CustomMenuItemShowsOpenInNewTab(item))

	on := true
	item.ShowOpenInNewTab = &on
	require.True(t, CustomMenuItemShowsOpenInNewTab(item))
}
