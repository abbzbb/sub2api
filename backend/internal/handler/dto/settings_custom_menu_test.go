package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomMenuItemHideOpenButton_DefaultFalse(t *testing.T) {
	raw := `{"id":"m1","label":"Help","icon_svg":"","url":"https://example.com","visibility":"user","sort_order":0}`
	var item CustomMenuItem
	require.NoError(t, json.Unmarshal([]byte(raw), &item))
	require.False(t, item.HideOpenButton)

	item.HideOpenButton = true
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"hide_open_button":true`)
}
