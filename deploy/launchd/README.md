# macOS launchd

This user LaunchAgent runs the example rule at five minutes past every hour and
once when loaded. Replace `REPLACE_ME` in the plist with the macOS short user
name; launchd does not expand `~` or `$HOME` in plist path values.

```sh
install -d -m 0755 "$HOME/.local/libexec" "$HOME/Library/LaunchAgents"
install -d -m 0700 "$HOME/Library/Logs/Venator" \
  "$HOME/.config/venator/rules"
install -m 0600 deploy/examples/global.yaml \
  "$HOME/.config/venator/global.yaml"
install -m 0600 deploy/examples/rule.yaml \
  "$HOME/.config/venator/rules/example.yaml"
install -m 0600 deploy/examples/events.ndjson \
  "$HOME/.config/venator/rules/events.ndjson"
install -m 0700 deploy/launchd/run-example.sh \
  "$HOME/.local/libexec/venator-run-example"
sed "s/REPLACE_ME/$(id -un)/g" \
  deploy/launchd/io.detect.venator.example.plist \
  > "$HOME/Library/LaunchAgents/io.detect.venator.example.plist"
chmod 0600 "$HOME/Library/LaunchAgents/io.detect.venator.example.plist"
plutil -lint "$HOME/Library/LaunchAgents/io.detect.venator.example.plist"
launchctl bootstrap "gui/$(id -u)" \
  "$HOME/Library/LaunchAgents/io.detect.venator.example.plist"
```

Run it immediately and inspect its status:

```sh
launchctl kickstart -k "gui/$(id -u)/io.detect.venator.example"
launchctl print "gui/$(id -u)/io.detect.venator.example"
```

To update an already loaded plist, boot it out, replace the file, and bootstrap
it again:

```sh
launchctl bootout "gui/$(id -u)/io.detect.venator.example"
```

The launcher optionally reads `$HOME/.config/venator/venator.env`; keep that
file mode `0600`. Use one copied plist and launcher per rule so every rule can
have its own calendar and observable exit status. `StartCalendarInterval` jobs
delayed by sleep run after the Mac wakes; they are not executed once per missed
interval.

The plist sets umask `077`, and the install steps make the log directory private
before launchd opens stdout/stderr. The sample rule reads the finite
`events.ndjson` file beside it, so a scheduled run exercises detection instead
of succeeding on empty stdin. A continuously growing production log needs a
collector with explicit offset and retry semantics.
