package views

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/plugin"
)

func makePluginView(panels []plugin.Panel, data map[string]json.RawMessage) *PluginTUIView {
	v := NewPluginTUIView(plugin.Plugin{Name: "test", Tab: "Test", Panels: panels})
	v.SetData(data)
	v.SetSize(80, 40)
	return v
}

func TestPluginTUIView_MetricRow(t *testing.T) {
	panels := []plugin.Panel{{ID: "m", Type: plugin.PanelMetricRow, Title: "Metrics"}}
	data := map[string]json.RawMessage{
		"m": json.RawMessage(`{"items":[{"label":"Count","value":42,"color":"teal"},{"label":"Rate","value":"95%","color":"green"}]}`),
	}
	v := makePluginView(panels, data)
	out := v.View()

	if !strings.Contains(out, "42") {
		t.Error("expected value 42 in output")
	}
	if !strings.Contains(out, "Count") {
		t.Error("expected label Count in output")
	}
	if !strings.Contains(out, "95%") {
		t.Error("expected value 95% in output")
	}
}

func TestPluginTUIView_Table(t *testing.T) {
	panels := []plugin.Panel{{ID: "t", Type: plugin.PanelTable, Title: "Data"}}
	data := map[string]json.RawMessage{
		"t": json.RawMessage(`{"columns":["Name","Score"],"rows":[{"cells":["Alice",95]},{"cells":["Bob",87]}]}`),
	}
	v := makePluginView(panels, data)
	out := v.View()

	if !strings.Contains(out, "Name") {
		t.Error("expected column header Name")
	}
	if !strings.Contains(out, "Alice") {
		t.Error("expected row value Alice")
	}
	if !strings.Contains(out, "87") {
		t.Error("expected row value 87")
	}
}

func TestPluginTUIView_BarChart(t *testing.T) {
	panels := []plugin.Panel{{ID: "b", Type: plugin.PanelBarChart, Title: "Chart"}}
	data := map[string]json.RawMessage{
		"b": json.RawMessage(`{"items":[{"label":"Bash","value":100},{"label":"Read","value":50}]}`),
	}
	v := makePluginView(panels, data)
	out := v.View()

	if !strings.Contains(out, "Bash") {
		t.Error("expected label Bash")
	}
	if !strings.Contains(out, "█") {
		t.Error("expected bar fill characters")
	}
	if !strings.Contains(out, "100") {
		t.Error("expected value 100")
	}
}

func TestPluginTUIView_BarChart_WithSecondary(t *testing.T) {
	panels := []plugin.Panel{{ID: "b", Type: plugin.PanelBarChart, Title: "Chart"}}
	data := map[string]json.RawMessage{
		"b": json.RawMessage(`{"items":[{"label":"skill-a","value":80,"secondary":6,"legend":["invocations","corrections"]}]}`),
	}
	v := makePluginView(panels, data)
	out := v.View()

	if !strings.Contains(out, "skill-a") {
		t.Error("expected label skill-a")
	}
	if !strings.Contains(out, "+6") {
		t.Error("expected secondary value +6")
	}
	if !strings.Contains(out, "invocations") {
		t.Error("expected legend entry")
	}
}

func TestPluginTUIView_List(t *testing.T) {
	panels := []plugin.Panel{{ID: "l", Type: plugin.PanelList, Title: "Items"}}
	data := map[string]json.RawMessage{
		"l": json.RawMessage(`{"items":[{"title":"Issue 1","subtitle":"High priority","body":"Details here","tags":["urgent"]}]}`),
	}
	v := makePluginView(panels, data)
	out := v.View()

	if !strings.Contains(out, "Issue 1") {
		t.Error("expected title Issue 1")
	}
	if !strings.Contains(out, "High priority") {
		t.Error("expected subtitle")
	}
	if !strings.Contains(out, "Details here") {
		t.Error("expected body")
	}
	if !strings.Contains(out, "urgent") {
		t.Error("expected tag")
	}
}

func TestPluginTUIView_EmptyData(t *testing.T) {
	panels := []plugin.Panel{{ID: "m", Type: plugin.PanelMetricRow, Title: "Metrics"}}
	v := makePluginView(panels, map[string]json.RawMessage{})
	out := v.View()

	if !strings.Contains(out, "No data") {
		t.Error("expected 'No data' for missing panel data")
	}
}

func TestPluginTUIView_NilData(t *testing.T) {
	v := NewPluginTUIView(plugin.Plugin{Name: "test", Panels: []plugin.Panel{{ID: "m", Type: plugin.PanelMetricRow}}})
	v.SetSize(80, 40)
	out := v.View()

	if !strings.Contains(out, "Loading") {
		t.Error("expected loading message when data is nil")
	}
}

func TestPluginTUIView_HalfWidthPanels(t *testing.T) {
	panels := []plugin.Panel{
		{ID: "a", Type: plugin.PanelMetricRow, Title: "Left", Width: "half"},
		{ID: "b", Type: plugin.PanelMetricRow, Title: "Right", Width: "half"},
	}
	data := map[string]json.RawMessage{
		"a": json.RawMessage(`{"items":[{"label":"L","value":1,"color":"teal"}]}`),
		"b": json.RawMessage(`{"items":[{"label":"R","value":2,"color":"green"}]}`),
	}
	v := makePluginView(panels, data)
	out := v.View()

	if !strings.Contains(out, "LEFT") && !strings.Contains(out, "Left") {
		t.Error("expected left panel title")
	}
	if !strings.Contains(out, "RIGHT") && !strings.Contains(out, "Right") {
		t.Error("expected right panel title")
	}
}

func TestPluginTUIView_Scroll(t *testing.T) {
	panels := []plugin.Panel{{ID: "t", Type: plugin.PanelTable, Title: "Data"}}
	var rows []json.RawMessage
	for i := 0; i < 30; i++ {
		rows = append(rows, json.RawMessage(`{"cells":["row","data"]}`))
	}
	rowsJSON, _ := json.Marshal(rows)
	data := map[string]json.RawMessage{
		"t": json.RawMessage(`{"columns":["A","B"],"rows":` + string(rowsJSON) + `}`),
	}
	v := makePluginView(panels, data)
	v.SetSize(80, 10)

	out1 := v.View()
	v.ScrollDown(5)
	out2 := v.View()

	if out1 == out2 {
		t.Error("expected different output after scrolling")
	}
}

func TestPluginTUIView_ScrollUpClamps(t *testing.T) {
	v := NewPluginTUIView(plugin.Plugin{Name: "test"})
	v.SetSize(80, 40)
	v.ScrollUp(100)
	if v.scroll != 0 {
		t.Errorf("expected scroll clamped to 0, got %d", v.scroll)
	}
}

func TestPluginTUIView_TableTruncatesRows(t *testing.T) {
	panels := []plugin.Panel{{ID: "t", Type: plugin.PanelTable, Title: "Big"}}
	var rowsSlice []map[string]interface{}
	for i := 0; i < 25; i++ {
		rowsSlice = append(rowsSlice, map[string]interface{}{"cells": []interface{}{"r", i}})
	}
	rowsJSON, _ := json.Marshal(rowsSlice)
	data := map[string]json.RawMessage{
		"t": json.RawMessage(`{"columns":["A","B"],"rows":` + string(rowsJSON) + `}`),
	}
	v := makePluginView(panels, data)
	out := v.View()

	if !strings.Contains(out, "5 more rows") {
		t.Error("expected truncation message for >20 rows")
	}
}

func TestPluginPickerView_Selection(t *testing.T) {
	plugins := []plugin.Plugin{
		{Name: "a", Tab: "Alpha"},
		{Name: "b", Tab: "Beta"},
	}
	picker := NewPluginPickerView(plugins)
	picker.SetSize(80, 20)

	sel := picker.Selected()
	if sel == nil || sel.Name != "a" {
		t.Error("expected first plugin selected by default")
	}

	picker.CursorDown()
	sel = picker.Selected()
	if sel == nil || sel.Name != "b" {
		t.Error("expected second plugin after CursorDown")
	}

	picker.CursorDown()
	sel = picker.Selected()
	if sel == nil || sel.Name != "b" {
		t.Error("expected cursor clamped at last item")
	}

	picker.CursorUp()
	sel = picker.Selected()
	if sel == nil || sel.Name != "a" {
		t.Error("expected first plugin after CursorUp")
	}

	picker.CursorUp()
	sel = picker.Selected()
	if sel == nil || sel.Name != "a" {
		t.Error("expected cursor clamped at first item")
	}
}

func TestPluginPickerView_EmptyPlugins(t *testing.T) {
	picker := NewPluginPickerView(nil)
	picker.SetSize(80, 20)

	out := picker.View()
	if !strings.Contains(out, "No plugins") {
		t.Error("expected no plugins message")
	}

	sel := picker.Selected()
	if sel != nil {
		t.Error("expected nil selection on empty list")
	}
}

func TestPluginPickerView_Render(t *testing.T) {
	plugins := []plugin.Plugin{
		{Name: "skill-dashboard", Tab: "Skill Dashboard", Panels: make([]plugin.Panel, 14)},
	}
	picker := NewPluginPickerView(plugins)
	picker.SetSize(80, 20)

	out := picker.View()
	if !strings.Contains(out, "Skill Dashboard") {
		t.Error("expected plugin tab name in output")
	}
	if !strings.Contains(out, "14 panels") {
		t.Error("expected panel count in output")
	}
}

func TestSortPlugins(t *testing.T) {
	plugins := []plugin.Plugin{
		{Name: "z", Tab: "Zeta"},
		{Name: "a", Tab: "Alpha"},
		{Name: "m", Tab: "Middle"},
	}
	SortPlugins(plugins)

	if plugins[0].Tab != "Alpha" || plugins[1].Tab != "Middle" || plugins[2].Tab != "Zeta" {
		t.Errorf("expected sorted order, got %s, %s, %s", plugins[0].Tab, plugins[1].Tab, plugins[2].Tab)
	}
}

func TestPadOrTrunc(t *testing.T) {
	if got := padOrTrunc("hello", 10); got != "hello     " {
		t.Errorf("expected padding, got %q", got)
	}
	if got := padOrTrunc("hello world", 8); got != "hello..." {
		t.Errorf("expected truncation, got %q", got)
	}
	if got := padOrTrunc("hi", 2); got != "hi" {
		t.Errorf("expected exact fit, got %q", got)
	}
}
