Output overview.gif
Set FontSize 14
Set Width 900
Set Height 700
Set Theme "Catppuccin Frappe"

# Run the dashboard with seeded data
Type "/Users/orlando/Projects/Personal/apiary/bin/apiary dashboard --config /tmp/apiary-screenshots/apiary.yaml"
Sleep 1s
Enter

# Wait for data to load and first refresh cycle
Sleep 7s

# Capture Overview tab
Screenshot overview.png

Sleep 1s

# Tasks tab (press Tab)
Tab
Sleep 4s
Screenshot tasks.png

# Select first task and show details
Sleep 1s
Down
Down
Sleep 1s
Type "d"
Sleep 3s
Screenshot tasks-detail.png

# Show task logs
Type "l"
Sleep 3s
Screenshot tasks-logs.png

# Go back to task list
Type "h"
Sleep 2s

# Switch to Agents tab
Tab
Sleep 4s
Screenshot agents.png

# Switch to Logs tab
Tab
Sleep 4s
Screenshot logs.png

# Quit
Type "q"
Sleep 1s
