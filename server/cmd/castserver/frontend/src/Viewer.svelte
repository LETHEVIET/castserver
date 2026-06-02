<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { Button } from 'flowbite-svelte';

  let ws: WebSocket | null = null;
  let frameSeq = 0;
  let renderedSeq = 0;
  let frameCount = 0;
  let byteCount = 0;
  let statsT = Date.now();
  let streaming = false;
  let showOverlay = true;
  let fullscreen = false;
  let idleText = 'Connecting...';
  let metricsText = '';

  let overlayTimer: ReturnType<typeof setTimeout>;
  let canvasEl: HTMLCanvasElement;
  let ctx: CanvasRenderingContext2D;
  let wakeLock: any = null;

  function showWithTimer() {
    showOverlay = true;
    clearTimeout(overlayTimer);
    overlayTimer = setTimeout(() => { showOverlay = false; }, 3000);
  }

  function toggleOverlay() {
    showOverlay = !showOverlay;
    if (showOverlay) {
      clearTimeout(overlayTimer);
      overlayTimer = setTimeout(() => { showOverlay = false; }, 3000);
    }
  }

  async function requestWakeLock() {
    if (!('wakeLock' in navigator)) return;
    try { wakeLock = await (navigator as any).wakeLock.request('screen'); } catch { }
  }

  function releaseWakeLock() {
    if (wakeLock) { wakeLock.release().then(() => { wakeLock = null; }).catch(() => { }); }
  }

  function isFullscreen() {
    return !!(document.fullscreenElement || (document as any).webkitFullscreenElement);
  }

  function toggleFullscreen() {
    if (isFullscreen()) {
      const exit = document.exitFullscreen || (document as any).webkitExitFullscreen;
      if (exit) try { exit.call(document); } catch { }
    } else {
      const wrap = document.getElementById('wrap');
      if (!wrap) return;
      const req = wrap.requestFullscreen || (wrap as any).webkitRequestFullscreen;
      if (req) try { req.call(wrap); } catch { }
    }
  }

  function syncFs() {
    fullscreen = isFullscreen();
    if (fullscreen) requestWakeLock();
    else releaseWakeLock();
  }

  function connect() {
    if (ws) return;
    idleText = 'Connecting...';
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    try {
      ws = new WebSocket(proto + '//' + window.location.host + '/ws/web');
    } catch {
      idleText = 'Connection failed';
      return;
    }

    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      frameSeq = 0; renderedSeq = 0;
      frameCount = 0; byteCount = 0; statsT = Date.now();
      idleText = 'Waiting for stream...';
    };

    ws.onmessage = (ev: MessageEvent) => {
      if (typeof ev.data === 'string') {
        try {
          const m = JSON.parse(ev.data);
          if (m.error) { idleText = m.error; ws!.close(); return; }
          streaming = m.status === 'streaming';
          if (!streaming) idleText = 'Waiting for cast...';
          else showWithTimer();
        } catch {}
        return;
      }

      const buf = ev.data as ArrayBuffer;
      if (buf.byteLength < 24) return;

      const seq = ++frameSeq;
      byteCount += buf.byteLength;
      const jpeg = new Uint8Array(buf, 24);
      const blob = new Blob([jpeg], { type: 'image/jpeg' });

      createImageBitmap(blob).then(bitmap => {
        if (seq < renderedSeq) { bitmap.close(); return; }
        renderedSeq = seq;
        if (!streaming) { streaming = true; showWithTimer(); }
        if (canvasEl.width !== bitmap.width || canvasEl.height !== bitmap.height) {
          canvasEl.width = bitmap.width;
          canvasEl.height = bitmap.height;
        }
        ctx.drawImage(bitmap, 0, 0);
        bitmap.close();
        frameCount++;
      }).catch(() => {});

      const now = Date.now();
      if (now - statsT > 1000) {
        const dt = (now - statsT) / 1000;
        const fps = (frameCount / dt).toFixed(1);
        const kbps = (byteCount * 8 / 1000 / dt).toFixed(0);
        metricsText = `${fps} FPS · ${Number(kbps) > 1000 ? (Number(kbps) / 1000).toFixed(1) + ' Mbps' : kbps + ' kbps'}`;
        frameCount = 0; byteCount = 0; statsT = now;
      }
    };

    ws.onerror = () => { idleText = 'Connection error'; };
    ws.onclose = () => { streaming = false; };
  }

  function disconnect() {
    if (ws) { try { ws.close(); } catch {} ws = null; }
    streaming = false;
    idleText = 'Disconnected';
    showOverlay = true;
  }

  onMount(() => {
    ctx = canvasEl.getContext('2d')!;
    document.addEventListener('fullscreenchange', syncFs);
    document.addEventListener('webkitfullscreenchange', syncFs);
    document.addEventListener('visibilitychange', async () => {
      if (document.visibilityState === 'visible' && isFullscreen()) await requestWakeLock();
    });
    connect();
  });

  onDestroy(() => {
    document.removeEventListener('fullscreenchange', syncFs);
    document.removeEventListener('webkitfullscreenchange', syncFs);
    disconnect();
    releaseWakeLock();
  });
</script>

<div id="wrap" class="fixed inset-0 flex items-center justify-center bg-black">
  <canvas bind:this={canvasEl} class="max-h-full max-w-full" class:hidden={!streaming}></canvas>

  <div class="absolute inset-0 flex flex-col items-center justify-center gap-4 bg-gray-950 z-10" class:hidden={streaming}>
    <div class="relative w-15 h-15 rounded-full bg-emerald-500/10 flex items-center justify-center">
      <div class="absolute inset-0 rounded-full border-2 border-emerald-500 animate-ping opacity-20"></div>
      <div class="w-3 h-3 rounded-full bg-emerald-500 shadow-[0_0_12px_#10b981]"></div>
    </div>
    <div class="text-xs text-gray-400 font-medium text-center max-w-64 leading-relaxed">{idleText}</div>
    {#if idleText === 'Disconnected' || idleText === 'Connection failed' || idleText === 'Connection error'}
      <Button on:click={connect}>Retry</Button>
    {/if}
  </div>

  <div
    class="absolute top-4 right-4 z-30 flex items-center gap-2 bg-black/60 backdrop-blur-xl border border-white/10 px-3 py-1.5 rounded-full text-[11px] font-medium tracking-wide text-gray-400 shadow-lg transition-opacity duration-300"
    class:hidden={!streaming || !showOverlay}
  >
    <span class="w-1.5 h-1.5 rounded-full bg-emerald-500 shadow-[0_0_8px_#10b981]"></span>
    {metricsText}
  </div>

  <div
    class="absolute bottom-6 left-1/2 -translate-x-1/2 z-30 flex items-center gap-2 bg-black/60 backdrop-blur-xl border border-white/10 px-3 py-2 rounded-full shadow-2xl transition-all duration-400"
    class:opacity-0={!showOverlay} class:pointer-events-none={!showOverlay} class:translate-y-5={!showOverlay}
  >
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <span on:click={(e) => e.stopPropagation()}>
      <Button color="red" size="xs" on:click={disconnect}>Disconnect</Button>
    </span>
    <span class="w-px h-5 bg-white/10"></span>
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <span on:click={(e) => { e.stopPropagation(); toggleFullscreen(); showWithTimer(); }}>
      <Button color="dark" size="xs" on:click={toggleFullscreen}>{fullscreen ? 'Exit Fullscreen' : 'Fullscreen'}</Button>
    </span>
  </div>

  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div class="absolute inset-0 z-5" on:click={toggleOverlay}></div>
</div>
