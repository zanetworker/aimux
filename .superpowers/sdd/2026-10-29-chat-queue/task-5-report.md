# Task 5 report: chat queue fixtures

Commit 47dcfb6 `test(clients): chat queue fixtures`. Files: api/testdata/clients/queue/*.json (31, generated once by a throwaway script, then hand-checkable) and a "Queue" section in api/testdata/clients/README.md.

Format: `{name, session{state, terminal?, queue?}, send?{error}, add?{error}, expect{composer, queue, items, keepText?, message?}}`.

Rules (all in the README): composer Send when free, Add to queue when busy/working/canType false or after 409 busy; secondary queue action always exists (README only); section visible only with items, paused shows reason verbatim + resume, running shows note; item labels Waiting/Sending/Answer in progress; edit/move only waiting; delete for waiting, or sending/answering only while paused; fixed messages for bad_text and queue_full (text kept).

Coverage: no queue, empty queue, free with items, working ACP and terminal, mac_in_use note, 17 paused-* reasons (every reason string in chatqueue), answering paused/running, sending running/paused, busy refusal, bad_text, queue_full.

Concerns: no Go test validates these files (the existing api tests only cover media/references), so only JSON validity was checked; go test ./api/... passes. Reason "In front" guard text "You're using this session on your Mac." from constraints is not a queue reason in code; the code's note is "Waiting: you're using this session on your Mac", used here. Client messages for bad_text/queue_full are my wording.

## Fix report (round 1)
Fixtures regenerated (queue/*.json, 36 files). working-terminal now canType true + working true; added blocked-disabled, terminal-unknown (unknown status -> Add to queue), paused-with-note (line = reason), done-item-hidden, delete-refused-sending (client message fixed); terminal-related paused-* files carry a realistic terminal object; bad_text message changed. README: finished "Rules" sentence, note/reason precedence, busy offer lifetime, unknown status, done items hidden, client-string note. Commit: test(clients): sharper chat queue fixtures.
