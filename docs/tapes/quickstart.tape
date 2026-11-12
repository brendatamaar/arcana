# Record with: scripts/record-tapes.sh (needs vhs, ttyd, ffmpeg and a Chrome).
Output docs/tapes/quickstart.gif
Set Shell "bash"
Set FontSize 15
Set Width 1100
Set Height 700
Set Padding 16
Set TypingSpeed 40ms
Set Theme "Dracula"

Hide
Type `export PS1='$ '` Enter
Type "cd $(mktemp -d)" Enter
Type "clear" Enter
Show

Type "# Create a sandbox, run code in it, move a file, clean up" Enter
Sleep 500ms
Type `id=$(arcana --json sandbox create --template python | jq -r .id); echo $id` Enter
Sleep 5s
Type `arcana exec $id -- python -c 'import sys; print(sys.version.split()[0], 1 + 1)'` Enter
Sleep 2s
Type `echo 'hello from the host' > note.txt` Enter
Type `arcana files put $id note.txt /workspace/note.txt` Enter
Sleep 1s
Type `arcana exec $id -- cat /workspace/note.txt` Enter
Sleep 2s
Type "# No network by default:" Enter
Type `arcana exec $id -- python -c 'import socket; socket.create_connection(("example.com", 80), 3)' 2>&1 | tail -2` Enter
Sleep 6s
Type `arcana sandbox rm $id` Enter
Sleep 2s
