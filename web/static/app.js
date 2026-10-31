// The server renders everything and owns the timer. This file only counts
// down between server round trips, chimes, and wires up drag-to-reorder.
'use strict';

const RING = 2 * Math.PI * 54;
let syncedFor = null; // endsAt we already asked the server to advance past

function fmtClock(ms) {
  const s = Math.max(0, Math.ceil(ms / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = String(s % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${String(m).padStart(2, '0')}:${sec}`;
}

function tick() {
  const el = document.getElementById('timer');
  if (!el) return;
  const d = el.dataset;
  const running = d.running === 'true';
  const ends = +d.ends, duration = +d.duration || 1;
  const left = running ? Math.max(0, ends - Date.now()) : +d.remaining;

  el.querySelector('.clock').textContent = fmtClock(left);
  el.querySelector('.ring-fill').style.strokeDashoffset = RING * (1 - left / duration);
  el.classList.toggle('final', running && left <= 60_000);
  el.classList.toggle('paused', d.phase !== 'idle' && !running);
  const title = el.querySelector('.current').textContent;
  document.title = d.phase === 'idle' ? 'pomo' : `${fmtClock(left)} · ${title}`;

  if (running && left <= 0 && syncedFor !== d.ends) {
    syncedFor = d.ends;
    phaseEnded(d.phase, d.sound === 'true');
    htmx.ajax('POST', '/timer/sync', { swap: 'none' });
  }
}

/* ---------- chime + notification ---------- */

let audio = null;
function primeAlerts() {
  if (!audio && window.AudioContext) audio = new AudioContext();
  if (audio && audio.state === 'suspended') audio.resume();
  if (window.Notification && Notification.permission === 'default') Notification.requestPermission();
}

function phaseEnded(phase, sound) {
  if (sound && audio) {
    const t0 = audio.currentTime;
    [660, 880, 1100].forEach((f, i) => {
      const o = audio.createOscillator(), g = audio.createGain();
      o.frequency.value = f;
      g.gain.setValueAtTime(0.0001, t0 + i * 0.18);
      g.gain.exponentialRampToValueAtTime(0.25, t0 + i * 0.18 + 0.02);
      g.gain.exponentialRampToValueAtTime(0.0001, t0 + i * 0.18 + 0.6);
      o.connect(g).connect(audio.destination);
      o.start(t0 + i * 0.18);
      o.stop(t0 + i * 0.18 + 0.65);
    });
  }
  if (window.Notification && Notification.permission === 'granted' && document.hidden) {
    const msg = phase === 'work' ? 'Pomodoro done. Take a break.' : 'Break over. Back to it.';
    try { new Notification('pomo', { body: msg, tag: 'pomo' }); } catch { /* mobile needs a service worker */ }
  }
}

/* ---------- queue ---------- */

function setupQueue(root) {
  const list = root.id === 'items' ? root : root.querySelector && root.querySelector('#items');
  if (list && !list.sortable) {
    list.sortable = Sortable.create(list, {
      handle: '.handle',
      draggable: '.item',
      animation: 150,
      onStart: () => list.classList.add('dragging'),
      onEnd: () => list.classList.remove('dragging'),
    });
  }
  // finish time in the browser's own clock
  root.querySelectorAll && root.querySelectorAll('[data-eta-min]').forEach((el) => {
    const at = new Date(Date.now() + +el.dataset.etaMin * 60_000);
    el.textContent = at.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  });
  const edit = root.querySelector && root.querySelector('.editing input[name="title"]');
  if (edit) { edit.focus(); edit.select(); }
}

// The 30s refresh skips while you're mid-edit or mid-drag.
window.pomoBusy = () => !!document.querySelector('.editing, .items.dragging') || document.getElementById('settings').open;

document.addEventListener('DOMContentLoaded', () => {
  htmx.onLoad(setupQueue);
  setupQueue(document);
  tick();
  setInterval(tick, 250);

  document.addEventListener('click', (e) => {
    if (e.target.closest('#timer button, .play-btn')) primeAlerts();
  });
  document.addEventListener('keydown', (e) => {
    if (e.target.closest('input, textarea') || document.getElementById('settings').open) {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && e.target.id === 'dump') e.target.form.requestSubmit();
      return;
    }
    if (e.code === 'Space' && !e.target.closest('button')) {
      e.preventDefault();
      primeAlerts();
      document.getElementById('toggle').click();
    }
  });
  // toasts fade out by CSS; drop them from the DOM afterwards
  document.addEventListener('animationend', (e) => {
    if (e.animationName === 'toast-out') e.target.remove();
  });
  document.body.addEventListener('htmx:sendError', () => {
    const t = document.createElement('div');
    t.className = 'toast err';
    t.textContent = "Can't reach the server";
    document.getElementById('toasts').appendChild(t);
  });
});
