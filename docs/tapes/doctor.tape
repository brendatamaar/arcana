# Record with: scripts/record-tapes.sh (needs vhs, ttyd, ffmpeg and a Chrome).
Output docs/tapes/doctor.gif
Set Shell "bash"
Set FontSize 15
Set Width 1100
Set Height 620
Set Padding 16
Set TypingSpeed 45ms
Set Theme "Dracula"
Set PlaybackSpeed 1.0

Hide
Type "export PS1='$ '" Enter
Type "clear" Enter
Show

Type "# Check that this host can run Arcana" Enter
Sleep 600ms
Type 'arcana doctor --config "$ARCANA_CONFIG"' Enter
Sleep 12s
