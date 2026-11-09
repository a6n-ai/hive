#!/bin/sh
# Test double for a headless agent. Prints a Claude-style result; with FAKE_ADD set,
# it adds a decision citing the first event in its input.
[ "$HIVE_DISABLED" = "1" ] || exit 3
IN=$(cat)
[ -n "$FAKE_SLEEP" ] && sleep "$FAKE_SLEEP"
if [ -n "$FAKE_ADD" ]; then
  EV=$(printf '%s' "$IN" | grep -o '"id":[0-9][0-9]*' | head -1 | cut -d: -f2)
  printf '{"type":"result","result":"{\\"ops\\":[{\\"op\\":\\"add\\",\\"type\\":\\"decision\\",\\"scope_kind\\":\\"project\\",\\"scope\\":\\"/p\\",\\"key\\":\\"e2e-decision\\",\\"text\\":\\"prefer small commits\\",\\"evidence\\":[%s]}]}"}\n' "$EV"
else
  printf '%s\n' '{"type":"result","result":"{\"ops\":[]}"}'
fi
