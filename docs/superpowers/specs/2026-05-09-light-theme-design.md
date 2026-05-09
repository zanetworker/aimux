# Light Theme for aimux Web Frontend

## Overview

Add a light theme to the aimux web dashboard using Red Hat brand colors. The existing dark theme stays as default. Users toggle between themes via a button in the header bar, with the preference persisted to localStorage.

## CSS Architecture

The current `:root` variables remain as the dark theme default. A `[data-theme="light"]` selector overrides all CSS custom properties with the light palette. No component code changes are needed since everything already references `var(--*)`.

Theme is applied by setting `document.documentElement.dataset.theme = "light" | "dark"`.

## Light Palette

All values sourced from Red Hat brand guidelines (brand.redhat.com).

| Variable | Dark (current) | Light | RH Token |
|----------|---------------|-------|----------|
| `--bg-0` | `#000000` | `#FFFFFF` | white |
| `--bg-1` | `#0d0d0d` | `#F2F2F2` | gray-10 |
| `--bg-2` | `#1a1a1a` | `#E0E0E0` | gray-20 |
| `--bg-3` | `#333333` | `#A3A3A3` | gray-40 |
| `--bg-4` | `#4d4d4d` | `#4D4D4D` | gray-60 |
| `--accent` | `#FF3131` | `#EE0000` | Red Hat Red |
| `--accent-dim` | `#3b1010` | `#FCE3E3` | red-10 |
| `--teal` | `#49D3B4` | `#37A3A3` | teal-50 |
| `--teal-dim` | `#0d2e26` | `#DAF2F2` | teal-10 |
| `--fg` | `#e6e6e6` | `#151515` | gray-95 |
| `--fg-2` | `#a6a6a6` | `#4D4D4D` | gray-60 |
| `--fg-3` | `#666666` | `#A3A3A3` | gray-40 |
| `--fg-4` | `#404040` | `#E0E0E0` | gray-20 |
| `--green` | `#69DF73` | `#2E8B38` | darker for white bg |
| `--green-dim` | `#142e17` | `#E6F5E8` | light green tint |
| `--orange` | `#FFB251` | `#F5921B` | orange-50 |
| `--orange-dim` | `#332210` | `#FFE8CC` | orange-10 |
| `--purple` | `#A772EF` | `#5E40BE` | purple-50 |
| `--purple-dim` | `#1f1433` | `#EDE8F5` | light purple tint |
| `--border` | `#1a1a1a` | `#E0E0E0` | gray-20 |
| `--border-hover` | `#333333` | `#A3A3A3` | gray-40 |

## Theme Toggle

A sun/moon icon button in the StatsBar header, placed between the Tasks button and the Launch button. Clicking toggles between dark and light themes.

## Persistence

Theme preference stored in `localStorage` key `aimux-theme`. On app mount, read the stored value and apply it to `document.documentElement.dataset.theme`. Default to `"dark"` if no stored preference.

## Files to Change

1. `web/src/styles/theme.css` -- add `[data-theme="light"]` block with all overrides
2. `web/src/components/StatsBar.tsx` -- add theme toggle button, accept `theme` and `onToggleTheme` props
3. `web/src/App.tsx` -- add theme state, localStorage read on mount, pass props to StatsBar

## What Does NOT Change

All other components. They already use CSS variables exclusively, so the theme swap is automatic.

## Testing

- Visual: toggle theme, verify all views (agents, sessions, plugins, trace panel, launch dialog, tasks panel) render correctly in both themes
- Persistence: refresh the page, verify theme preference is retained
- Default: clear localStorage, verify dark theme is default
