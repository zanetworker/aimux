package views

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/plugin"
)

var (
	pluginTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5F87FF")).MarginBottom(1)
	pluginSectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB")).MarginTop(1)
	pluginDescStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Italic(true)
	pluginChipLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	pluginBarFill     = lipgloss.NewStyle().Foreground(lipgloss.Color("#37A3A3"))
	pluginBarSecondary = lipgloss.NewStyle().Foreground(lipgloss.Color("#5E40BE"))
	pluginListTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB"))
	pluginListSub     = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	pluginListBody    = lipgloss.NewStyle().Foreground(lipgloss.Color("#D1D5DB"))
	pluginTagStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5921B"))
	pluginNoData      = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Italic(true)
)

// PluginTUIView renders a plugin's panels in the terminal.
type PluginTUIView struct {
	manifest plugin.Plugin
	data     map[string]json.RawMessage
	width    int
	height   int
	scroll   int
}

// NewPluginTUIView creates a view for the given plugin manifest.
func NewPluginTUIView(manifest plugin.Plugin) *PluginTUIView {
	return &PluginTUIView{manifest: manifest}
}

// SetData updates the panel data from the executor.
func (v *PluginTUIView) SetData(data map[string]json.RawMessage) {
	v.data = data
	v.scroll = 0
}

// SetSize sets the available terminal dimensions.
func (v *PluginTUIView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// ScrollDown moves the viewport down.
func (v *PluginTUIView) ScrollDown(n int) {
	v.scroll += n
}

// ScrollUp moves the viewport up.
func (v *PluginTUIView) ScrollUp(n int) {
	v.scroll -= n
	if v.scroll < 0 {
		v.scroll = 0
	}
}

// Manifest returns the plugin's manifest for the caller.
func (v *PluginTUIView) Manifest() plugin.Plugin {
	return v.manifest
}

// View renders all panels as a scrollable vertical layout.
func (v *PluginTUIView) View() string {
	if v.data == nil {
		return pluginNoData.Render("  Loading plugin data...")
	}

	var sections []string

	i := 0
	for i < len(v.manifest.Panels) {
		p := v.manifest.Panels[i]

		if p.Width == "half" && i+1 < len(v.manifest.Panels) && v.manifest.Panels[i+1].Width == "half" {
			p2 := v.manifest.Panels[i+1]
			leftW := v.width/2 - 2
			rightW := v.width - leftW - 3
			left := v.renderPanel(p, leftW)
			right := v.renderPanel(p2, rightW)
			sections = append(sections, lipgloss.JoinHorizontal(lipgloss.Top, left, "   ", right))
			i += 2
		} else {
			sections = append(sections, v.renderPanel(p, v.width-2))
			i++
		}
	}

	full := strings.Join(sections, "\n\n")
	lines := strings.Split(full, "\n")

	if v.scroll >= len(lines) {
		v.scroll = max(0, len(lines)-1)
	}

	visible := v.height - 1
	if visible < 1 {
		visible = 1
	}

	end := v.scroll + visible
	if end > len(lines) {
		end = len(lines)
	}
	if v.scroll < end {
		lines = lines[v.scroll:end]
	}

	return strings.Join(lines, "\n")
}

func (v *PluginTUIView) renderPanel(p plugin.Panel, w int) string {
	var b strings.Builder

	header := pluginSectionStyle.Render(strings.ToUpper(p.Title))
	b.WriteString(header)
	b.WriteString("\n")

	if p.Description != "" {
		desc := p.Description
		if len(desc) > w && w > 3 {
			desc = desc[:w-3] + "..."
		}
		b.WriteString(pluginDescStyle.Render(desc))
		b.WriteString("\n")
	}

	raw, ok := v.data[p.ID]
	if !ok {
		b.WriteString(pluginNoData.Render("No data"))
		return b.String()
	}

	switch p.Type {
	case plugin.PanelMetricRow:
		b.WriteString(pluginRenderMetricRow(raw))
	case plugin.PanelTable:
		b.WriteString(pluginRenderTable(raw, w))
	case plugin.PanelBarChart:
		b.WriteString(pluginRenderBarChart(raw, w))
	case plugin.PanelList:
		b.WriteString(pluginRenderList(raw))
	default:
		b.WriteString(pluginNoData.Render(fmt.Sprintf("Unknown panel type: %s", p.Type)))
	}

	return b.String()
}

func pluginRenderMetricRow(raw json.RawMessage) string {
	var d struct {
		Items []plugin.MetricItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Items) == 0 {
		return pluginNoData.Render("No metrics")
	}

	var chips []string
	for _, item := range d.Items {
		valStr := fmt.Sprintf("%v", item.Value)
		valStyle := chipValueStyle(item.Color)
		chip := valStyle.Render(valStr) + " " + pluginChipLabel.Render(item.Label)
		chips = append(chips, chip)
	}
	return strings.Join(chips, "  ")
}

func chipValueStyle(color string) lipgloss.Style {
	colorMap := map[string]string{
		"green": "#22C55E", "accent": "#EF4444", "orange": "#F5921B",
		"purple": "#5E40BE", "teal": "#37A3A3",
	}
	hex, ok := colorMap[color]
	if !ok {
		hex = "#E5E7EB"
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(hex))
}

func pluginRenderTable(raw json.RawMessage, w int) string {
	var d plugin.TableData
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Columns) == 0 {
		return pluginNoData.Render("No table data")
	}

	colCount := len(d.Columns)
	colWidths := computeColumnWidths(d, w, colCount)

	var b strings.Builder

	var headerParts []string
	for ci, col := range d.Columns {
		headerParts = append(headerParts, padOrTrunc(col, colWidths[ci]))
	}
	headerLine := costHeaderStyle.Render(strings.Join(headerParts, " "))
	b.WriteString(headerLine)
	b.WriteString("\n")

	maxRows := 20
	rows := d.Rows
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}

	for _, row := range rows {
		var parts []string
		for ci := 0; ci < colCount; ci++ {
			val := ""
			if ci < len(row.Cells) {
				val = fmt.Sprintf("%v", row.Cells[ci])
			}
			parts = append(parts, padOrTrunc(val, colWidths[ci]))
		}
		rowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#D1D5DB"))
		if row.Color != "" {
			if hex, ok := map[string]string{
				"green": "#22C55E", "accent": "#EF4444", "orange": "#F5921B",
				"teal": "#37A3A3", "fg-3": "#9CA3AF",
			}[row.Color]; ok {
				rowStyle = rowStyle.Foreground(lipgloss.Color(hex))
			}
		}
		b.WriteString(rowStyle.Render(strings.Join(parts, " ")))
		b.WriteString("\n")
	}

	if len(d.Rows) > maxRows {
		b.WriteString(pluginNoData.Render(fmt.Sprintf("  ... and %d more rows", len(d.Rows)-maxRows)))
	}

	return b.String()
}

func computeColumnWidths(d plugin.TableData, totalW, colCount int) []int {
	widths := make([]int, colCount)
	for ci, col := range d.Columns {
		widths[ci] = len(col)
	}
	for _, row := range d.Rows {
		for ci := 0; ci < colCount && ci < len(row.Cells); ci++ {
			l := len(fmt.Sprintf("%v", row.Cells[ci]))
			if l > widths[ci] {
				widths[ci] = l
			}
		}
	}

	available := totalW - (colCount - 1)
	total := 0
	for _, w := range widths {
		total += w
	}
	if total > available && available > colCount {
		for ci := range widths {
			widths[ci] = widths[ci] * available / total
			if widths[ci] < 4 {
				widths[ci] = 4
			}
		}
	}

	return widths
}

func padOrTrunc(s string, w int) string {
	if len(s) > w {
		if w > 3 {
			return s[:w-3] + "..."
		}
		return s[:w]
	}
	return s + strings.Repeat(" ", w-len(s))
}

func pluginRenderBarChart(raw json.RawMessage, totalW int) string {
	var d struct {
		Items []plugin.BarChartItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Items) == 0 {
		return pluginNoData.Render("No chart data")
	}

	labelW := 0
	for _, item := range d.Items {
		if len(item.Label) > labelW {
			labelW = len(item.Label)
		}
	}
	if labelW > 30 {
		labelW = 30
	}

	barW := totalW - labelW - 12
	if barW < 10 {
		barW = 10
	}

	maxVal := 0.0
	for _, item := range d.Items {
		v := item.Value + item.Secondary
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal == 0 {
		maxVal = 1
	}

	var b strings.Builder
	for _, item := range d.Items {
		label := padOrTrunc(item.Label, labelW)

		primaryLen := int(item.Value / maxVal * float64(barW))
		secondaryLen := int(item.Secondary / maxVal * float64(barW))

		bar := pluginBarFill.Render(strings.Repeat("█", primaryLen))
		if secondaryLen > 0 {
			bar += pluginBarSecondary.Render(strings.Repeat("█", secondaryLen))
		}

		valStr := fmt.Sprintf("%6.0f", item.Value)
		if item.Secondary > 0 {
			valStr += fmt.Sprintf("+%.0f", item.Secondary)
		}

		fmt.Fprintf(&b, "%s %s %s\n", label, bar, costMutedStyle.Render(valStr))
	}

	if len(d.Items) > 0 && len(d.Items[0].Legend) > 0 {
		var legendParts []string
		colors := []lipgloss.Style{pluginBarFill, pluginBarSecondary}
		for i, l := range d.Items[0].Legend {
			colorIdx := i % len(colors)
			legendParts = append(legendParts, colors[colorIdx].Render("█")+" "+l)
		}
		b.WriteString(strings.Repeat(" ", labelW+1) + strings.Join(legendParts, "  "))
		b.WriteString("\n")
	}

	return b.String()
}

func pluginRenderList(raw json.RawMessage) string {
	var d struct {
		Items []plugin.ListItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Items) == 0 {
		return pluginNoData.Render("No items")
	}

	var b strings.Builder
	maxItems := 15
	items := d.Items
	if len(items) > maxItems {
		items = items[:maxItems]
	}

	for _, item := range items {
		title := pluginListTitle.Render("  " + item.Title)
		if len(item.Tags) > 0 {
			tags := pluginTagStyle.Render("[" + strings.Join(item.Tags, ", ") + "]")
			title += " " + tags
		}
		b.WriteString(title)
		b.WriteString("\n")

		if item.Subtitle != "" {
			b.WriteString(pluginListSub.Render("    " + item.Subtitle))
			b.WriteString("\n")
		}
		if item.Body != "" {
			body := item.Body
			if len(body) > 120 {
				body = body[:117] + "..."
			}
			b.WriteString(pluginListBody.Render("    " + body))
			b.WriteString("\n")
		}
	}

	if len(d.Items) > maxItems {
		b.WriteString(pluginNoData.Render(fmt.Sprintf("  ... and %d more", len(d.Items)-maxItems)))
		b.WriteString("\n")
	}

	return b.String()
}

// PluginPickerView renders a simple list of available plugins for selection.
type PluginPickerView struct {
	plugins  []plugin.Plugin
	cursor   int
	width    int
	height   int
}

// NewPluginPickerView creates a picker for selecting among multiple plugins.
func NewPluginPickerView(plugins []plugin.Plugin) *PluginPickerView {
	return &PluginPickerView{plugins: plugins}
}

// SetSize sets the picker dimensions.
func (v *PluginPickerView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// CursorUp moves the picker cursor up.
func (v *PluginPickerView) CursorUp() {
	if v.cursor > 0 {
		v.cursor--
	}
}

// CursorDown moves the picker cursor down.
func (v *PluginPickerView) CursorDown() {
	if v.cursor < len(v.plugins)-1 {
		v.cursor++
	}
}

// Selected returns the currently highlighted plugin.
func (v *PluginPickerView) Selected() *plugin.Plugin {
	if v.cursor < len(v.plugins) {
		return &v.plugins[v.cursor]
	}
	return nil
}

// PluginCount returns how many plugins are available.
func (v *PluginPickerView) PluginCount() int {
	return len(v.plugins)
}

// View renders the picker list.
func (v *PluginPickerView) View() string {
	if len(v.plugins) == 0 {
		return pluginNoData.Render("  No plugins available.")
	}

	var b strings.Builder
	b.WriteString(pluginTitleStyle.Render("  Select Plugin"))
	b.WriteString("\n\n")

	for i, p := range v.plugins {
		cursor := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("#D1D5DB"))
		if i == v.cursor {
			cursor = "> "
			style = lipgloss.NewStyle().Foreground(lipgloss.Color("#37A3A3")).Bold(true)
		}
		panels := fmt.Sprintf("%d panels", len(p.Panels))
		line := fmt.Sprintf("%s%-20s %s", cursor, p.Tab, pluginDescStyle.Render(panels))
		b.WriteString(style.Render(line))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(pluginDescStyle.Render("  Enter:select  Esc:back"))

	return b.String()
}

// SortPlugins sorts plugins alphabetically by tab name.
func SortPlugins(plugins []plugin.Plugin) {
	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].Tab < plugins[j].Tab
	})
}
