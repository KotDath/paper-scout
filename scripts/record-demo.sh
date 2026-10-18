#!/usr/bin/env bash
# Capture only the Android display. UI actions belong to claude-in-mobile.
set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "Usage: $0 DEVICE artifacts/clip.mp4 [seconds; 0 only if device supports it]" >&2
  exit 2
fi
device=$1
output=$(realpath -m -- "$2")
seconds=${3:-180}
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
[[ $seconds =~ ^[0-9]+$ ]] || { echo 'Invalid duration' >&2; exit 2; }
case "$output" in
  "$root/artifacts/"*.mp4) ;;
  *) echo 'Output must be an MP4 under this worktree artifacts/' >&2; exit 2 ;;
esac
[[ ! -e $output ]] || { echo 'Output already exists' >&2; exit 2; }
for tool in adb ffprobe; do command -v "$tool" >/dev/null; done
mkdir -p -- "$(dirname -- "$output")"
tag="paper-scout-$(date -u +%Y%m%dT%H%M%S)-$$"
remote="/sdcard/$tag.mp4"
pidfile="/data/local/tmp/$tag.pid"
capture_pid=''
remote_pid=''

stop_capture() {
  if [[ $remote_pid =~ ^[0-9]+$ ]]; then
    adb -s "$device" shell "kill -INT $remote_pid" >/dev/null 2>&1 || true
  fi
}
cleanup() {
  stop_capture
  if [[ -n $capture_pid ]]; then wait "$capture_pid" 2>/dev/null || true; fi
  adb -s "$device" shell "rm -f $pidfile $remote" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap stop_capture INT TERM

# The unique PID belongs to this recording; never kill another recorder.
adb -s "$device" shell \
  "screenrecord --time-limit $seconds --bit-rate 4000000 $remote & p=\$!; echo \$p > $pidfile; wait \$p" &
capture_pid=$!
for ((attempt=0; attempt<50; attempt++)); do
  remote_pid=$(adb -s "$device" shell "cat $pidfile" 2>/dev/null | tr -d '\r') || true
  size=$(adb -s "$device" shell "stat -c %s $remote" 2>/dev/null | tr -d '\r') || true
  if [[ $remote_pid =~ ^[0-9]+$ && $size =~ ^[0-9]+$ && $size -gt 48 ]]; then
    echo "Recording verified: $remote ($size bytes). Send SIGINT to this script to finish."
    break
  fi
  kill -0 "$capture_pid" 2>/dev/null || { echo 'Recorder exited before capture was verified' >&2; exit 1; }
  sleep 0.2
done
[[ ${size:-0} =~ ^[0-9]+$ && ${size:-0} -gt 48 ]] || { echo 'Capture did not grow' >&2; exit 1; }
result=0
wait "$capture_pid" || result=$?
# A signal interrupts bash wait; wait again until the MP4 is finalized.
if kill -0 "$capture_pid" 2>/dev/null; then wait "$capture_pid" || result=$?; fi
capture_pid=''
remote_pid=''
[[ $result == 0 || $result == 130 || $result == 143 ]] || { echo "Recorder failed: $result" >&2; exit 1; }
adb -s "$device" pull "$remote" "$output"
[[ -s $output ]]
duration=$(ffprobe -v error -select_streams v:0 -show_entries stream=duration -of default=nw=1:nk=1 "$output")
awk -v duration="$duration" 'BEGIN { exit !(duration + 0 > 0) }' || {
  echo 'Capture has no positive duration; retained locally for diagnosis' >&2
  exit 1
}
ffprobe -v error -select_streams v:0 -show_entries stream=codec_name,width,height:format=duration,size -of json "$output"
echo "Saved: $output"
