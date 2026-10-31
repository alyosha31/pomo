'use strict';

const $ = (sel, el = document) => el.querySelector(sel);
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const ICON = {
  grip: '<svg viewBox="0 0 24 24"><circle cx="9" cy="6" r="1"/><circle cx="15" cy="6" r="1"/><circle cx="9" cy="12" r="1"/><circle cx="15" cy="12" r="1"/><circle cx="9" cy="18" r="1"/><circle cx="15" cy="18" r="1"/></svg>',
  check: '<svg viewBox="0 0 24 24"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>',
  play: '<svg viewBox="0 0 24 24"><path d="M7 4.5v15l12-7.5z"/></svg>',
  trash: '<svg viewBox="0 0 24 24"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg>',
};

let state = null;
let session = { authRequired: false, planner: 'none' };
const ui = { editing: null, dragging: false, showDone: false, fresh: new Set(), finishing: false };

/* ---------- api ---------- */

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  const data = await res.json().catch(() => ({}));
  if (res.status === 401 && path !== '/api/login') {
    showLogin();
    throw new Error('Session expired, log in again');
  }
  if (!res.ok) throw new Error(data.error || res.statusText);
  if (data.state) {
    state = data.state;
    render();
  }
  return data;
}

// Fire-and-report: actions from buttons shouldn't throw into the void.
async function act(fn) {
  try { return await fn(); } catch (e) { toast(e.message, 'err'); }
}

/* ---------- helpers ---------- */

const now = () => Date.now();
const today = () => new Date().toLocaleDateString('en-CA');
const openTasks = () => state.tasks.filter((t) => !t.done);
const findTask = (id) => state.tasks.find((t) => t.id === id);
const phaseMs = (phase) => ({ work: state.settings.workMin, short: state.settings.shortMin, long: state.settings.longMin }[phase] || state.settings.workMin) * 60000;

function fmtClock(ms) {
  const s = Math.max(0, Math.ceil(ms / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  const mm = String(m).padStart(h ? 2 : 1, '0'), ss = String(sec).padStart(2, '0');
  return h ? `${h}:${mm}:${ss}` : `${mm.padStart(2, '0')}:${ss}`;
}

function fmtDuration(min) {
  min = Math.round(min);
  const h = Math.floor(min / 60), m = min % 60;
  if (!h) return `${m}m`;
  return m ? `${h}h ${m}m` : `${h}h`;
}

function fmtTime(date) {
  return date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
}

/* ---------- timer ---------- */

const timer = () => state.timer;
const isRunning = () => timer().phase !== 'idle' && timer().endsAt > 0;
function remaining() {
  const t = timer();
  if (t.phase === 'idle') return phaseMs('work');
  return t.endsAt ? Math.max(0, t.endsAt - now()) : t.remaining;
}

function makePhase(phase, taskId, run) {
  const d = phaseMs(phase);
  return { phase, taskId, duration: d, endsAt: run ? now() + d : 0, remaining: d, askDone: false };
}

async function saveTimer(t) {
  state.timer = t;
  render();
  await api('PUT', '/api/timer', t);
}

async function startTask(id) {
  primeAlerts();
  await saveTimer(makePhase('work', id, true));
}

async function toggleTimer() {
  const t = timer();
  if (t.phase === 'idle') {
    const next = openTasks()[0];
    if (!next) return toast('Add a task first');
    return startTask(next.id);
  }
  primeAlerts();
  if (isRunning()) await saveTimer({ ...t, remaining: Math.max(0, t.endsAt - now()), endsAt: 0 });
  else await saveTimer({ ...t, endsAt: now() + t.remaining });
}

async function resetTimer() {
  await saveTimer({ phase: 'idle', taskId: '', duration: 0, endsAt: 0, remaining: 0, askDone: false });
}

// finishPhase moves to the next phase. credit=false is a skip: a skipped
// focus session doesn't count as a pomodoro.
async function finishPhase(credit) {
  const t = timer();
  if (t.phase === 'work') {
    if (credit) {
      await api('POST', '/api/pomodoro', { taskId: t.taskId, day: today(), endsAt: t.endsAt });
    }
    const task = findTask(t.taskId);
    const reached = !!task && !task.done && task.completed >= task.pomodoros;
    const next = makePhase(reached || !task || task.done ? 'long' : 'short', t.taskId, state.settings.autoBreaks);
    next.askDone = credit && reached;
    if (credit) alertUser('Pomodoro done', next.phase === 'long' ? `Take ${state.settings.longMin} minutes.` : `Take ${state.settings.shortMin}.`);
    await saveTimer(next);
    return;
  }
  // break over: continue the same task unless it's finished
  let task = findTask(t.taskId);
  if (!task || task.done) task = openTasks()[0];
  if (!task) {
    if (credit) alertUser('Break over', 'Queue is empty. Nice work.');
    return resetTimer();
  }
  if (credit) alertUser('Break over', `Next: ${task.title}`);
  await saveTimer(makePhase('work', task.id, state.settings.autoWork));
}

async function answerDone(markDone) {
  const t = timer();
  const task = findTask(t.taskId);
  if (task) {
    if (markDone) await api('PATCH', `/api/tasks/${task.id}`, { done: true });
    else await api('PATCH', `/api/tasks/${task.id}`, { pomodoros: task.pomodoros + 1 });
  }
  const next = { ...timer(), askDone: false };
  if (!markDone && next.phase === 'long') {
    // more of the same task: shrink the long break to a short one
    const elapsed = next.duration - remaining();
    const short = phaseMs('short');
    const left = Math.max(0, short - elapsed);
    Object.assign(next, { phase: 'short', duration: short, remaining: left, endsAt: next.endsAt ? now() + left : 0 });
  }
  await saveTimer(next);
}

function tick() {
  if (!state) return;
  if (isRunning() && remaining() <= 0 && !ui.finishing) {
    ui.finishing = true;
    finishPhase(true).catch((e) => toast(e.message, 'err')).finally(() => { ui.finishing = false; });
  }
  renderClock();
}

/* ---------- alerts ---------- */

let audioCtx = null;
function primeAlerts() {
  if (!audioCtx && window.AudioContext) audioCtx = new AudioContext();
  if (audioCtx && audioCtx.state === 'suspended') audioCtx.resume();
  if (window.Notification && Notification.permission === 'default') Notification.requestPermission();
}

function chime() {
  if (!audioCtx || !state.settings.sound) return;
  const t0 = audioCtx.currentTime;
  [660, 880, 1100].forEach((f, i) => {
    const o = audioCtx.createOscillator(), g = audioCtx.createGain();
    o.type = 'sine';
    o.frequency.value = f;
    g.gain.setValueAtTime(0.0001, t0 + i * 0.18);
    g.gain.exponentialRampToValueAtTime(0.25, t0 + i * 0.18 + 0.02);
    g.gain.exponentialRampToValueAtTime(0.0001, t0 + i * 0.18 + 0.6);
    o.connect(g).connect(audioCtx.destination);
    o.start(t0 + i * 0.18);
    o.stop(t0 + i * 0.18 + 0.65);
  });
}

function alertUser(title, body) {
  chime();
  if (window.Notification && Notification.permission === 'granted' && document.hidden) {
    try { new Notification(title, { body, icon: 'favicon.svg', tag: 'pomo' }); } catch { /* some mobile browsers only notify from a service worker */ }
  }
}

/* ---------- render ---------- */

const PHASE_LABEL = { idle: 'Ready', work: 'Focus', short: 'Short break', long: 'Long break' };
const RING = 2 * Math.PI * 54;

function render() {
  if (!state) return;
  renderHeader();
  renderTimer();
  if (!ui.dragging && !ui.editing) renderTasks();
  else renderSummary();
}

function renderHeader() {
  const n = state.stats[today()] || 0;
  $('#today').innerHTML = `<span class="brand-dot" style="width:9px;height:9px"></span><b>${n}</b> today`;
}

function renderClock() {
  const t = timer();
  const left = remaining();
  const total = t.phase === 'idle' ? phaseMs('work') : t.duration || 1;
  $('#clock').textContent = fmtClock(left);
  const fill = $('#ring-fill');
  fill.style.strokeDasharray = RING;
  fill.style.strokeDashoffset = t.phase === 'idle' ? 0 : RING * (1 - left / total);
  document.body.classList.toggle('paused', t.phase !== 'idle' && !isRunning());
  const task = findTask(t.taskId);
  document.title = t.phase === 'idle' ? 'pomo' : `${fmtClock(left)} · ${t.phase === 'work' ? (task ? task.title : 'Focus') : PHASE_LABEL[t.phase]}`;
}

function renderTimer() {
  const t = timer();
  document.body.dataset.phase = t.phase;
  $('#phase-label').textContent = PHASE_LABEL[t.phase] + (t.phase !== 'idle' && !isRunning() ? ' · paused' : '');
  const task = t.phase === 'idle' ? openTasks()[0] : findTask(t.taskId);
  const cur = $('#current-task');
  if (task) {
    cur.textContent = (t.phase === 'idle' ? 'Up next: ' : t.phase === 'work' ? '' : 'After the break: ') + task.title;
    cur.classList.remove('none');
    const n = task.completed + 1;
    $('#clock-sub').textContent = t.phase === 'work' ? `pomodoro ${n} of ${Math.max(task.pomodoros, n)}` : `${task.completed}/${task.pomodoros} done`;
  } else {
    cur.textContent = t.phase === 'idle' ? 'Nothing queued yet' : 'Task removed';
    cur.classList.add('none');
    $('#clock-sub').textContent = '';
  }
  $('#toggle-btn').textContent = isRunning() ? 'Pause' : t.phase !== 'idle' && t.remaining < t.duration ? 'Resume' : 'Start';
  $('#reset-btn').disabled = t.phase === 'idle';
  $('#skip-btn').disabled = t.phase === 'idle';
  $('#ask').hidden = !t.askDone || !task || task.done;
  if (task) $('#ask-title').textContent = task.title;
  renderClock();
}

// Minutes left in the queue, including the breaks between pomodoros and tasks.
function queueMinutes() {
  const s = state.settings, t = timer();
  let total = 0, pomos = 0, count = 0;
  for (const task of openTasks()) {
    const left = Math.max(task.pomodoros - task.completed, task.id === t.taskId && t.phase !== 'work' ? 0 : 1);
    pomos += left;
    if (left === 0) continue;
    if (count > 0) total += s.longMin;
    total += left * s.workMin + (left - 1) * s.shortMin;
    count++;
  }
  if (t.phase === 'work' && findTask(t.taskId) && !findTask(t.taskId).done) total -= (t.duration - remaining()) / 60000;
  else if (t.phase !== 'idle') total += remaining() / 60000;
  return { total: Math.max(0, total), pomos, count };
}

function renderSummary() {
  const open = openTasks();
  if (!open.length) { $('#summary').textContent = state.tasks.length ? 'All clear.' : ''; return; }
  const { total, pomos } = queueMinutes();
  const eta = new Date(now() + total * 60000);
  $('#summary').innerHTML = `<b>${open.length}</b> task${open.length === 1 ? '' : 's'} · <b>${pomos}</b> pomodoro${pomos === 1 ? '' : 's'} · ${fmtDuration(total)} · done ≈ <b>${fmtTime(eta)}</b>`;
}

function dotsHTML(task) {
  const dots = [];
  const n = Math.max(task.pomodoros, task.completed);
  for (let i = 0; i < Math.min(n, 12); i++) {
    const cls = i < task.completed ? (i >= task.pomodoros ? 'dot extra' : 'dot full') : 'dot';
    dots.push(`<span class="${cls}"></span>`);
  }
  if (n > 12) dots.push(`<span>+${n - 12}</span>`);
  return dots.join('');
}

function taskHTML(task) {
  const active = timer().phase !== 'idle' && timer().taskId === task.id;
  const badge = task.source === 'ai' ? '<span class="badge" title="Estimated by AI">AI est.</span>'
    : task.source === 'default' ? '<span class="badge guess" title="No estimate could be inferred — set one">set estimate</span>' : '';
  const minutes = task.pomodoros * state.settings.workMin;
  return `<li class="task${task.done ? ' done' : ''}${active ? ' active' : ''}${ui.fresh.has(task.id) ? ' fresh' : ''}" data-id="${task.id}">
    ${task.done ? '<span></span>' : `<button class="handle" aria-label="Drag to reorder" title="Drag to reorder">${ICON.grip}</button>`}
    <button class="check" data-act="done" aria-label="${task.done ? 'Mark not done' : 'Mark done'}">${ICON.check}</button>
    <div class="body">
      <div class="title" data-act="edit">${esc(task.title)}</div>
      ${task.notes ? `<div class="notes" data-act="edit">${esc(task.notes)}</div>` : ''}
      <div class="meta">
        <span class="dots" title="${task.completed} of ${task.pomodoros} pomodoros">${dotsHTML(task)}</span>
        ${task.done ? '' : `<span class="stepper"><button data-act="minus" aria-label="One fewer pomodoro">−</button><span>${task.pomodoros}</span><button data-act="plus" aria-label="One more pomodoro">+</button></span>`}
        <span>${fmtDuration(minutes)}</span>
        ${task.done ? '' : badge}
      </div>
    </div>
    <div class="actions">
      ${task.done ? '' : `<button class="icon-btn play-btn" data-act="play" aria-label="Focus on this now" title="Focus on this now">${ICON.play}</button>`}
      <button class="icon-btn del-btn" data-act="delete" aria-label="Delete" title="Delete">${ICON.trash}</button>
    </div>
  </li>`;
}

function renderTasks() {
  const open = openTasks();
  const done = state.tasks.filter((t) => t.done);
  $('#tasks').innerHTML = open.map(taskHTML).join('');
  $('#empty').hidden = state.tasks.length > 0;
  $('#done-section').hidden = done.length === 0;
  $('#done-toggle').textContent = `${ui.showDone ? '▾' : '▸'} Done (${done.length})`;
  $('#done-list').hidden = !ui.showDone;
  $('#done-list').innerHTML = done.map(taskHTML).join('') + (done.length ? '<li><button class="btn ghost small" data-act="clear-done">Clear done tasks</button></li>' : '');
  ui.fresh.clear();
  renderSummary();
}

/* ---------- task actions ---------- */

async function onTaskClick(e) {
  const el = e.target.closest('[data-act]');
  if (!el) return;
  const act_ = el.dataset.act;
  if (act_ === 'clear-done') return act(() => api('POST', '/api/clear-done'));
  const li = el.closest('.task');
  const task = li && findTask(li.dataset.id);
  if (!task) return;
  switch (act_) {
    case 'done':
      return act(async () => {
        await api('PATCH', `/api/tasks/${task.id}`, { done: !task.done });
        if (!task.done && timer().taskId === task.id && timer().askDone) await saveTimer({ ...timer(), askDone: false });
      });
    case 'plus':
      return act(() => api('PATCH', `/api/tasks/${task.id}`, { pomodoros: task.pomodoros + 1 }));
    case 'minus':
      if (task.pomodoros > 1) return act(() => api('PATCH', `/api/tasks/${task.id}`, { pomodoros: task.pomodoros - 1 }));
      return;
    case 'play':
      return act(() => startTask(task.id));
    case 'delete':
      return act(async () => {
        const index = state.tasks.indexOf(task);
        await api('DELETE', `/api/tasks/${task.id}`);
        toast(`Deleted “${task.title}”`, '', 'Undo', () => act(() => api('POST', '/api/tasks', { ...task, index })));
      });
    case 'edit':
      if (!task.done) startEdit(li, task);
  }
}

function startEdit(li, task) {
  if (ui.editing) return;
  ui.editing = task.id;
  const body = $('.body', li);
  body.innerHTML = `<form class="edit-form">
    <input name="title" value="${esc(task.title)}" maxlength="140" required>
    <textarea name="notes" rows="2" placeholder="Notes" maxlength="500">${esc(task.notes)}</textarea>
    <div class="edit-actions"><button type="button" class="btn ghost small" data-cancel>Cancel</button><button class="btn primary small">Save</button></div>
  </form>`;
  const form = $('form', body);
  const input = form.elements.title;
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
  const close = () => { ui.editing = null; renderTasks(); };
  $('[data-cancel]', form).onclick = close;
  form.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') close();
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) form.requestSubmit();
  });
  form.onsubmit = async (e) => {
    e.preventDefault();
    ui.editing = null;
    await act(() => api('PATCH', `/api/tasks/${task.id}`, { title: input.value, notes: form.elements.notes.value }));
    renderTasks();
  };
}

async function addTask() {
  const data = await act(() => api('POST', '/api/tasks', { title: 'New task', pomodoros: 1 }));
  if (!data) return;
  const task = state.tasks[state.tasks.length - 1];
  const li = $(`.task[data-id="${task.id}"]`);
  if (li) {
    li.scrollIntoView({ block: 'center', behavior: 'smooth' });
    startEdit(li, task);
    $('input', li).select();
  }
}

/* ---------- drag to reorder (pointer events, works with touch) ---------- */

function onDragStart(e) {
  const handle = e.target.closest('.handle');
  if (!handle || e.button > 0 || ui.editing) return;
  e.preventDefault();
  const li = handle.closest('.task');
  const list = li.parentElement;
  const rect = li.getBoundingClientRect();
  const offsetY = e.clientY - rect.top;
  const ph = document.createElement('li');
  ph.className = 'placeholder';
  ph.style.height = rect.height + 'px';
  li.before(ph);
  Object.assign(li.style, { width: rect.width + 'px', left: rect.left + 'px', top: rect.top + 'px' });
  li.classList.add('dragging');
  ui.dragging = true;
  let lastY = e.clientY, scrollTimer = null;

  const place = (y) => {
    li.style.top = y - offsetY + 'px';
    const items = [...list.querySelectorAll('.task:not(.dragging)')];
    const before = items.find((it) => {
      const r = it.getBoundingClientRect();
      return y < r.top + r.height / 2;
    });
    if (before) { if (before.previousElementSibling !== ph) list.insertBefore(ph, before); }
    else if (list.lastElementChild !== ph) list.appendChild(ph);
  };
  const autoscroll = () => {
    const edge = 70;
    const dy = lastY < edge ? -12 : lastY > window.innerHeight - edge ? 12 : 0;
    if (dy) { window.scrollBy(0, dy); place(lastY); }
    scrollTimer = requestAnimationFrame(autoscroll);
  };
  scrollTimer = requestAnimationFrame(autoscroll);

  const move = (ev) => { lastY = ev.clientY; place(ev.clientY); };
  const up = () => {
    cancelAnimationFrame(scrollTimer);
    window.removeEventListener('pointermove', move);
    window.removeEventListener('pointerup', up);
    window.removeEventListener('pointercancel', up);
    ph.replaceWith(li);
    li.classList.remove('dragging');
    li.removeAttribute('style');
    ui.dragging = false;
    const ids = [...list.querySelectorAll('.task')].map((it) => it.dataset.id);
    const before = openTasks().map((t) => t.id);
    if (ids.join() === before.join()) return renderTasks();
    // optimistic: reorder locally, then persist (done tasks stay at the end)
    const done = state.tasks.filter((t) => t.done);
    state.tasks = ids.map(findTask).concat(done);
    renderTasks();
    act(() => api('PUT', '/api/order', { ids: state.tasks.map((t) => t.id) }));
  };
  window.addEventListener('pointermove', move);
  window.addEventListener('pointerup', up);
  window.addEventListener('pointercancel', up);
}

/* ---------- composer + voice ---------- */

const SpeechRecognition = window.SpeechRecognition || window.webkitSpeechRecognition;
let rec = null;

function autosize() {
  const ta = $('#dump');
  ta.style.height = 'auto';
  ta.style.height = Math.max(120, ta.scrollHeight + 2) + 'px';
}

function toggleMic() {
  if (rec) { rec.stop(); return; }
  const ta = $('#dump');
  const base = ta.value.trim() ? ta.value.trimEnd() + ' ' : '';
  rec = new SpeechRecognition();
  rec.continuous = true;
  rec.interimResults = true;
  rec.lang = navigator.language || 'en-US';
  rec.onresult = (e) => {
    let text = '';
    for (const r of e.results) text += r[0].transcript;
    ta.value = base + text.trim();
    autosize();
  };
  rec.onerror = (e) => {
    if (e.error === 'not-allowed') toast('Microphone blocked. Allow it in the browser (needs HTTPS).', 'err');
    else if (e.error !== 'no-speech' && e.error !== 'aborted') toast('Dictation error: ' + e.error, 'err');
  };
  rec.onend = () => {
    rec = null;
    $('#mic-btn').classList.remove('live');
    $('#composer-hint').textContent = '';
  };
  rec.start();
  $('#mic-btn').classList.add('live');
  $('#composer-hint').textContent = 'Listening… tap the mic to stop';
}

async function submitPlan(e) {
  e.preventDefault();
  if (rec) rec.stop();
  const ta = $('#dump');
  const text = ta.value.trim();
  if (!text) { ta.focus(); return; }
  const btn = $('#plan-btn');
  btn.classList.add('loading');
  btn.textContent = 'Planning…';
  $('#composer-hint').textContent = session.planner === 'none' ? '' : `Asking ${session.planner}…`;
  try {
    const data = await api('POST', '/api/plan', { text });
    data.added.forEach((id) => ui.fresh.add(id));
    renderTasks();
    ta.value = '';
    autosize();
    toast(`Added ${data.added.length} task${data.added.length === 1 ? '' : 's'}`);
    if (data.warning) toast(data.warning, 'warn');
  } catch (err) {
    toast(err.message, 'err');
  } finally {
    btn.classList.remove('loading');
    btn.textContent = 'Plan tasks';
    $('#composer-hint').textContent = '';
  }
}

/* ---------- settings ---------- */

function openSettings() {
  const f = $('#settings-form');
  for (const [k, v] of Object.entries(state.settings)) {
    const el = f.elements[k];
    if (!el) continue;
    if (el.type === 'checkbox') el.checked = v; else el.value = v;
  }
  $('#planner-info').textContent = session.planner === 'none'
    ? 'No AI CLI found on the server, so dumps are split line by line.'
    : `Planning with the ${session.planner} CLI on the server.`;
  $('#settings').showModal();
}

async function saveSettings() {
  const f = $('#settings-form');
  const s = {
    workMin: +f.elements.workMin.value,
    shortMin: +f.elements.shortMin.value,
    longMin: +f.elements.longMin.value,
    autoBreaks: f.elements.autoBreaks.checked,
    autoWork: f.elements.autoWork.checked,
    sound: f.elements.sound.checked,
  };
  await act(() => api('PUT', '/api/settings', s));
  // an idle or paused-at-start phase picks up the new length right away
  const t = timer();
  if (t.phase !== 'idle' && !t.endsAt && t.remaining === t.duration) await act(() => saveTimer(makePhase(t.phase, t.taskId, false)));
}

/* ---------- toasts ---------- */

function toast(msg, kind = '', actionLabel, onAction) {
  const el = document.createElement('div');
  el.className = 'toast ' + kind;
  el.innerHTML = `<span></span>`;
  el.firstChild.textContent = msg;
  if (actionLabel) {
    const b = document.createElement('button');
    b.textContent = actionLabel;
    b.onclick = () => { el.remove(); onAction(); };
    el.appendChild(b);
  }
  $('#toasts').appendChild(el);
  setTimeout(() => el.remove(), kind === 'warn' || kind === 'err' ? 8000 : actionLabel ? 6000 : 3000);
}

/* ---------- boot ---------- */

function showLogin() {
  $('#app').hidden = true;
  $('#login').hidden = false;
  $('#password').focus();
}

async function login(e) {
  e.preventDefault();
  $('#login-error').hidden = true;
  try {
    await api('POST', '/api/login', { password: $('#password').value });
    $('#password').value = '';
    await boot();
  } catch (err) {
    $('#login-error').textContent = err.message;
    $('#login-error').hidden = false;
  }
}

async function refresh() {
  if (!state || ui.dragging || ui.editing || ui.finishing) return;
  try { await api('GET', '/api/state'); } catch { /* offline: keep local state */ }
}

async function boot() {
  session = await (await fetch('/api/session', { credentials: 'same-origin' })).json();
  if (!session.authed) return showLogin();
  $('#login').hidden = true;
  $('#app').hidden = false;
  $('#logout-btn').hidden = !session.authRequired;
  await api('GET', '/api/state');
  // a phase that ended while the page was closed
  tick();
}

function bind() {
  $('#login-form').addEventListener('submit', login);
  $('#toggle-btn').onclick = () => act(toggleTimer);
  $('#reset-btn').onclick = () => act(resetTimer);
  $('#skip-btn').onclick = () => act(() => finishPhase(false));
  $('#ask-done').onclick = () => act(() => answerDone(true));
  $('#ask-more').onclick = () => act(() => answerDone(false));
  $('#composer').addEventListener('submit', submitPlan);
  $('#dump').addEventListener('input', autosize);
  $('#dump').addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) $('#composer').requestSubmit();
  });
  $('#add-btn').onclick = addTask;
  $('#settings-btn').onclick = openSettings;
  $('#settings').addEventListener('close', () => { if ($('#settings').returnValue === 'save') saveSettings(); });
  $('#logout-btn').onclick = async () => { await fetch('/api/logout', { method: 'POST' }); state = null; showLogin(); };
  $('#done-toggle').onclick = () => { ui.showDone = !ui.showDone; renderTasks(); };
  for (const list of [$('#tasks'), $('#done-list')]) list.addEventListener('click', onTaskClick);
  $('#tasks').addEventListener('pointerdown', onDragStart);
  if (SpeechRecognition) {
    $('#mic-btn').hidden = false;
    $('#mic-btn').onclick = toggleMic;
  }
  document.addEventListener('keydown', (e) => {
    const typing = e.target.closest('input, textarea, [contenteditable]') || $('#settings').open;
    if (e.code === 'Space' && !typing && state && !e.target.closest('button')) {
      e.preventDefault();
      act(toggleTimer);
    }
  });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
  setInterval(tick, 250);
  setInterval(refresh, 20000);
}

bind();
boot().catch((e) => toast(e.message, 'err'));
