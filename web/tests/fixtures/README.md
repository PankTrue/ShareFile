# Browser media fixture

`preview.mp4` is a generated two-second blue 320×180 video, H.264/yuv420p in MP4 with the index at the start. It has no audio or third-party content. The browser tests use it to verify decoding, playback, seeking, downloads, and cleanup without requiring FFmpeg on the test host.

To regenerate from this directory:

```sh
ffmpeg -hide_banner -loglevel error -f lavfi \
  -i 'color=c=0x3478d5:s=320x180:r=25:d=2' \
  -c:v libx264 -pix_fmt yuv420p -movflags +faststart preview.mp4
```
