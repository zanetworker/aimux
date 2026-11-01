# README demo for aimux. Recorded in a container by record.sh, so only the
# seeded demo agents appear. Re-record after UI changes: ./docs/demo/record.sh
Output "/tmp/demo.mp4"

Set Shell bash
Set FontFamily "JetBrains Mono"
Set FontSize 16
Set Width 1360
Set Height 760
Set Padding 18
Set Margin 22
Set MarginFill "#FFFFFF"
Set BorderRadius 12
Set WindowBar Colorful
Set TypingSpeed 60ms
Set Theme { "name": "aimux", "background": "#0F1623", "foreground": "#C9D1D9", "cursor": "#6B95FF", "selection": "#22314B", "black": "#0F1623", "brightBlack": "#5B6B88", "red": "#F47067", "brightRed": "#FF938A", "green": "#2FBF71", "brightGreen": "#57D68D", "yellow": "#F2B33D", "brightYellow": "#F7C96B", "blue": "#6B95FF", "brightBlue": "#91B0FF", "magenta": "#B392F0", "brightMagenta": "#CDB4FF", "cyan": "#56D4DD", "brightCyan": "#7FE3EA", "white": "#C9D1D9", "brightWhite": "#F0F3F6" }

# Start on the agent list so the GIF's first frame shows the product.
Hide
Type "clear && aimux" Enter
Sleep 2.5s
Show

# 1. Every agent, one list
Sleep 3s

# 2. Split view: the active agent's trace beside its live session
Enter
Sleep 3s
Type "k" Sleep 500ms Enter
Sleep 3.5s

# 3. Label the turn (a cycles GOOD, BAD, WASTE)
Type "a"
Sleep 2.5s

# 4. Cost across every agent (Ctrl+G leaves the split view; Esc goes to the agent)
Ctrl+G
Sleep 1.2s
Type "c"
Sleep 3s
Escape
Sleep 800ms
Type "q"
Sleep 400ms
