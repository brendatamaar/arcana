# Record with: scripts/record-tapes.sh (needs vhs, ttyd, ffmpeg and a Chrome).
Output docs/tapes/fork.gif
Set Shell "bash"
Set FontSize 14
Set Width 1200
Set Height 760
Set Padding 16
Set TypingSpeed 35ms
Set Theme "Dracula"

Hide
Type `export PS1='$ '` Enter
Type "set +m" Enter
Type "cd $(mktemp -d)" Enter
Type "clear" Enter
Show

Type "# Prepare a workspace once, snapshot it, then try three variants in parallel" Enter
Sleep 500ms
Type `id=$(arcana --json sandbox create --template python | jq -r .id)` Enter
Sleep 5s
Type `arcana exec $id -- sh -c 'echo base > /workspace/state.txt'` Enter
Sleep 2s
Type `snap=$(arcana --json snapshot create $id | jq -r .id); arcana snapshot list` Enter
Sleep 4s
Type `arcana --json fork $snap --count 3 | jq -r '.items[].id' | tee forks.txt` Enter
Sleep 12s
Type `for f in $(cat forks.txt); do arcana exec $f -- sh -c "echo variant-$RANDOM >> /workspace/state.txt; tr '\n' ' ' < /workspace/state.txt; echo" & done; wait` Enter
Sleep 5s
Type `echo '--- the original is untouched:'; arcana exec $id -- cat /workspace/state.txt` Enter
Sleep 3s
Type `for f in $(cat forks.txt) $id; do arcana sandbox rm $f > /dev/null; done; arcana snapshot rm $snap` Enter
Sleep 3s
