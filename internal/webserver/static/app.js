'use strict';

/* ── State ─────────────────────────────────────────────────────────────── */
let ws, activePeer = null, contacts = [], myOnion = '', myPeerID = '';
let activeTransfers = {}; // id -> { name, sent, total, speedKBs, done, isUpload }
let currentMessages = [], hasMoreMessages = true, loadingHistory = false;

/* ── Settings State ── */
let debugMode = localStorage.getItem('debugMode') === 'true';
let hideSidebarSetting = localStorage.getItem('hideSidebarSetting') === 'true';
let chatFontSize = localStorage.getItem('chatFontSize') || '16px';
let replyToID = null;
let replyToBody = null;
let selectedMsgID = null;
let selectedMsgBody = null;

/* ── WebRTC Call State ── */
let peerConnection = null;
let localStream = null;
let callTimerInterval = null;
let callStartTime = null;
let callType = null;
let callPeer = null;
let incomingCallSignal = null;

/* ── Boot ──────────────────────────────────────────────────────────────── */
document.addEventListener('DOMContentLoaded', () => {
  // On mobile, start with sidebar open so they can see contacts list
  if (window.innerWidth <= 680) {
    document.getElementById('sidebar').classList.add('open');
  }
  
  connectWS();
  setInterval(fetchStatus, 15000);

  // Apply settings on boot
  document.documentElement.style.setProperty('--chat-font-size', chatFontSize);
  applySidebarState();
  
  document.getElementById('contact-search')
    .addEventListener('input', () => renderList(filteredContacts()));
  
  document.querySelectorAll('.overlay').forEach(el =>
    el.addEventListener('click', e => { if (e.target === el) el.classList.remove('open'); })
  );

  // Setup infinite scroll for messages
  const msgEl = document.getElementById('messages');
  msgEl.addEventListener('scroll', () => {
    if (msgEl.scrollTop === 0 && !loadingHistory && hasMoreMessages && activePeer) {
      loadMoreMsgs();
    }
  });

  // Close dropdown on click outside
  window.addEventListener('click', e => {
    const menu = document.getElementById('header-dropdown');
    if (menu && !e.target.closest('.dropdown-wrap')) {
      menu.classList.remove('show');
    }
  });
});

/* ── WebSocket ─────────────────────────────────────────────────────────── */
function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.onopen    = () => logBar('Connected', 'ok');
  ws.onmessage = e => dispatch(JSON.parse(e.data));
  ws.onclose   = () => { logBar('Disconnected — retrying…', 'err'); setTimeout(connectWS, 3000); };
  ws.onerror   = () => ws.close();
}

function dispatch(ev) {
  switch (ev.type) {
    case 'snapshot':
      myOnion = ev.payload.me || '';
      myPeerID = ev.payload.peer_id || '';
      document.getElementById('my-onion').textContent = myOnion ? myOnion.slice(0, 16) + '…' : '—';
      document.getElementById('my-peer-id').textContent = myPeerID ? myPeerID.slice(0, 16) + '…' : '—';
      setContacts(ev.payload.contacts || []);
      fetchStatus();
      break;
    case 'identity':
      myOnion = ev.payload.onion || '';
      myPeerID = ev.payload.peer_id || '';
      document.getElementById('my-onion').textContent = myOnion ? myOnion.slice(0, 16) + '…' : '—';
      document.getElementById('my-peer-id').textContent = myPeerID ? myPeerID.slice(0, 16) + '…' : '—';
      break;
    case 'contacts_updated':
      setContacts(ev.payload || []);
      break;
    case 'message':
      if (activePeer && ev.payload.peer === activePeer) {
        currentMessages = ev.payload.messages || [];
        renderMessages(currentMessages);
        setTimeout(scrollToBottom, 50);
        setTimeout(scrollToBottom, 150);
      }
      markBadge(ev.payload.peer, ev.payload.messages || []);
      break;
    case 'message_deleted':
      if (activePeer && ev.payload.peer === activePeer) {
        currentMessages = currentMessages.filter(m => m.id !== ev.payload.id);
        renderMessages(currentMessages, true);
      }
      break;
    case 'message_starred':
      if (activePeer && ev.payload.peer === activePeer) {
        currentMessages = currentMessages.map(m => {
          if (m.id === ev.payload.id) {
            m.starred = ev.payload.starred;
          }
          return m;
        });
        renderMessages(currentMessages, true);
      }
      break;
    case 'db_cleared':
      currentMessages = [];
      contacts = [];
      setContacts([]);
      renderMessages([]);
      break;
    case 'chat_cleared':
      if (activePeer && ev.payload.peer === activePeer) {
        currentMessages = [];
        renderMessages([]);
      }
      break;
    case 'file_progress':
      handleFileProgress(ev.payload, false);
      break;
    case 'file_upload_progress':
      handleFileProgress(ev.payload, true);
      break;
    case 'log':
      logBar(ev.payload);
      break;
    case 'call_signaling':
      handleCallSignaling(ev.payload);
      break;
  }
}

/* ── Status ────────────────────────────────────────────────────────────── */
async function fetchStatus() {
  try {
    const d = await get('/api/status');
    const on = d.tor && d.tor !== 'offline';
    document.getElementById('tor-dot').classList.toggle('on', on);
    document.getElementById('tor-lbl').textContent = on ? 'online' : 'offline';
  } catch {}
}

/* ── Contacts ──────────────────────────────────────────────────────────── */
function setContacts(list) {
  contacts = list || [];
  renderList(filteredContacts());
}

function filteredContacts() {
  const q = document.getElementById('contact-search').value.toLowerCase();
  return q ? contacts.filter(c => (c.nickname||'').toLowerCase().includes(q) || c.id.toLowerCase().includes(q))
           : contacts;
}

function renderList(list) {
  const el = document.getElementById('contact-list');
  el.innerHTML = '';
  if (!list.length) {
    el.innerHTML = '<div style="padding:20px;text-align:center;color:var(--muted);font-size:13px">No contacts yet.</div>';
    return;
  }
  list.forEach(c => el.appendChild(makeContactEl(c)));
}

function makeContactEl(c) {
  const name    = c.nickname || c.id.slice(0, 16) + '…';
  const initial = (c.nickname || 'K')[0].toUpperCase();
  const div = document.createElement('div');
  div.className = 'c-item' + (c.id === activePeer ? ' active' : '');
  div.dataset.id = c.id;
  div.innerHTML = `
    <div class="ava">${initial}</div>
    <div class="c-info">
      <div class="c-name">${esc(name)}</div>
      <div class="c-sub">${c.id.slice(0, 26)}…</div>
    </div>`;
  div.onclick = () => selectContact(c);
  return div;
}

function selectContact(c) {
  activePeer = c.id;
  document.querySelectorAll('.c-item').forEach(el =>
    el.classList.toggle('active', el.dataset.id === c.id)
  );
  document.getElementById('chat-ava').textContent = (c.nickname || 'K')[0].toUpperCase();
  document.getElementById('chat-peer-name').textContent = c.nickname || c.id.slice(0, 20) + '…';
  document.getElementById('chat-peer-status').textContent = c.id;
  document.getElementById('no-chat').style.display  = 'none';
  document.getElementById('chat-panel').style.display = 'flex';
  
  // Close sidebar on mobile once a chat is tapped
  document.getElementById('sidebar').classList.remove('open');
  
  // Reset input and buttons
  document.getElementById('msg-input').value = '';
  handleInputEvent();
  
  // Reset pagination state
  currentMessages = [];
  hasMoreMessages = true;
  loadingHistory = false;
  
  loadMsgs(c.id);
}

/* ── Messages ──────────────────────────────────────────────────────────── */
async function loadMsgs(peer) {
  loadingHistory = true;
  try {
    const msgs = await get(`/api/messages?peer=${encodeURIComponent(peer)}&offset=0&limit=30`);
    currentMessages = Array.isArray(msgs) ? msgs : [];
    if (currentMessages.length < 30) {
      hasMoreMessages = false;
    }
    renderMessages(currentMessages, false);
  } catch (e) {
    console.error("Error loading messages", e);
  } finally {
    loadingHistory = false;
  }
}

async function loadMoreMsgs() {
  if (loadingHistory || !hasMoreMessages || !activePeer) return;
  loadingHistory = true;
  
  const el = document.getElementById('messages');
  const prevScrollHeight = el.scrollHeight;
  
  try {
    const nextOffset = currentMessages.length;
    const msgs = await get(`/api/messages?peer=${encodeURIComponent(activePeer)}&offset=${nextOffset}&limit=30`);
    if (Array.isArray(msgs) && msgs.length > 0) {
      currentMessages = msgs.concat(currentMessages);
      renderMessages(currentMessages, true); // true = preserve scroll position
      
      // Restore scroll offset so the view doesn't jump
      el.scrollTop = el.scrollHeight - prevScrollHeight;
      
      if (msgs.length < 30) {
        hasMoreMessages = false;
      }
    } else {
      hasMoreMessages = false;
    }
  } catch (e) {
    console.error("Error loading more messages", e);
  } finally {
    loadingHistory = false;
  }
}

function isInternalMessage(body) {
  if (!body) return false;
  return body.startsWith('[fstart:') ||
         body.startsWith('[fchunk:') ||
         body.startsWith('[fend:') ||
         body.startsWith('[call-sig:') ||
         body.startsWith('[call-stream:');
}

function renderMessages(msgs, preserveScroll) {
  const el = document.getElementById('messages');
  const prevScrollTop = el.scrollTop;
  const prevScrollHeight = el.scrollHeight;
  
  el.innerHTML = '';
  let lastDate = '';
  
  (msgs || []).forEach(m => {
    if (isInternalMessage(m.body) && !debugMode) {
      return; // Skip rendering internal protocol messages if debug mode is off
    }
    
    const d = new Date(m.ts);
    const ds = isNaN(d) ? '' : d.toLocaleDateString([], {day:'numeric',month:'short',year:'numeric'});
    if (ds && ds !== lastDate) {
      lastDate = ds;
      const div = document.createElement('div');
      div.className = 'date-div';
      div.innerHTML = `<span>${ds}</span>`;
      el.appendChild(div);
    }
    el.appendChild(buildBubble(m));
  });
  
  // Append active transfer overlays to the messages container if they are for this peer
  Object.keys(activeTransfers).forEach(id => {
    const x = activeTransfers[id];
    if (x.peer === activePeer) {
      el.appendChild(buildTransferBubble(id, x));
    }
  });

  if (!preserveScroll) {
    scrollToBottom();
  }
}

function scrollToBottom() {
  const el = document.getElementById('messages');
  if (el) {
    el.scrollTop = el.scrollHeight;
  }
}

function buildBubble(m) {
  const sent = m.from === 'me';
  const row   = document.createElement('div');
  row.className = `msg-row ${sent ? 'sent' : 'recv'}`;

  const bub = document.createElement('div');
  bub.className = `bubble ${sent ? 'sent' : 'recv'}`;
  bub.dataset.id = m.id;

  // Render reply quote box if this is a reply message
  if (m.reply_to_id) {
    const q = document.createElement('div');
    q.className = 'bubble-reply-quote';
    q.innerText = m.reply_to_body || 'Original Message';
    q.onclick = (e) => {
      e.stopPropagation();
      scrollToMessage(m.reply_to_id);
    };
    bub.appendChild(q);
  }

  // media-sent
  const sentMatch = (m.body || '').match(/^\[media-sent:(.+)\]$/);
  // media-recv
  const recvMatch = (m.body || '').match(/^\[media-recv:(.+)\]$/);

  if (sentMatch) {
    bub.classList.add('media-b');
    bub.appendChild(mediaEl(sentMatch[1], m.media_name || sentMatch[1], m.media_type || '', true));
  } else if (recvMatch) {
    bub.classList.add('media-b');
    bub.appendChild(mediaEl(recvMatch[1], m.media_name || recvMatch[1], m.media_type || '', false));
  } else {
    const p = document.createElement('div');
    p.innerHTML = formatMessageBody(m.body || '');
    bub.appendChild(p);
    
    // Parse URLs and render link previews
    const urlRegex = /(https?:\/\/[^\s<]+)/g;
    const urls = (m.body || '').match(urlRegex);
    if (urls) {
      const uniqueUrls = [...new Set(urls)]; // Deduplicate
      uniqueUrls.forEach(url => {
        const previewContainer = document.createElement('div');
        bub.appendChild(previewContainer);
        renderLinkPreview(url, previewContainer);
      });
    }
  }

  const ts   = m.ts ? new Date(m.ts).toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'}) : '';
  const tick = sent ? (m.read ? '<span class="tick del">✓✓</span>' : '<span class="tick">✓</span>') : '';
  
  // Render star indicator
  const star = m.starred ? '<span class="star-icon">★</span>' : '';
  
  const meta = document.createElement('div');
  meta.className = 'msg-meta';
  meta.innerHTML = `${star}${ts} ${tick}`;
  bub.appendChild(meta);

  // Add click and context menu handlers for message bubble options
  bub.addEventListener('contextmenu', e => {
    e.preventDefault();
    showContextMenu(e, m);
  });
  bub.addEventListener('click', e => {
    if (e.target.tagName !== 'A' && e.target.tagName !== 'IMG' && e.target.tagName !== 'VIDEO') {
      e.stopPropagation();
      showContextMenu(e, m);
    }
  });

  row.appendChild(bub);
  return row;
}

function mediaEl(path, origName, mimeType, isSent) {
  const url = `/api/media/${path}`;
  const ext  = origName.split('.').pop().toLowerCase();
  const wrap = document.createElement('div');

  if (['jpg','jpeg','png','gif','webp','svg','avif'].includes(ext)) {
    const img = document.createElement('img');
    img.src = url; img.loading = 'lazy';
    img.onclick = () => window.open(url, '_blank');
    img.onload = scrollToBottom;
    wrap.appendChild(img);
    wrap.appendChild(dlLink(url, origName));
  } else if (['mp4','webm','mov','mkv','avi'].includes(ext)) {
    const vid = document.createElement('video');
    vid.src = url; vid.controls = true; vid.preload = 'metadata';
    vid.onloadedmetadata = scrollToBottom;
    wrap.appendChild(vid);
    wrap.appendChild(dlLink(url, origName));
  } else if (['mp3','ogg','m4a','aac','wav','flac','opus'].includes(ext)) {
    const aud = document.createElement('audio');
    aud.src = url; aud.controls = true;
    wrap.appendChild(aud);
    wrap.appendChild(dlLink(url, origName));
  } else {
    const card = document.createElement('div');
    card.className = 'file-card';
    card.onclick   = () => dlFile(url, origName);
    card.innerHTML = `
      <div class="file-icon">${fileIcon(ext)}</div>
      <div class="file-info">
        <div class="file-name">${esc(origName)}</div>
        <div class="file-dl">Tap to download</div>
      </div>`;
    wrap.appendChild(card);
  }
  return wrap;
}

function dlLink(url, name) {
  const a = document.createElement('div');
  a.style.cssText = 'font-size:11px;color:var(--accent-h);cursor:pointer;margin-top:4px';
  a.textContent   = '⬇ Download ' + name;
  a.onclick = () => dlFile(url, name);
  return a;
}

function fileIcon(ext) {
  if (['pdf'].includes(ext))                               return '📄';
  if (['doc','docx','odt','txt','md'].includes(ext))      return '📝';
  if (['xls','xlsx','csv'].includes(ext))                  return '📊';
  if (['zip','tar','gz','rar','7z'].includes(ext))         return '🗜';
  if (['apk'].includes(ext))                               return '📦';
  return '📎';
}

function markBadge(peer, msgs) {
  const unread = (msgs || []).filter(m => m.from !== 'me' && !m.read).length;
  const item = document.querySelector(`.c-item[data-id="${CSS.escape(peer)}"]`);
  if (!item) return;
  let b = item.querySelector('.c-badge');
  if (unread > 0) {
    if (!b) { b = document.createElement('span'); b.className = 'c-badge'; item.appendChild(b); }
    b.textContent = unread;
  } else if (b) b.remove();
}

/* ── File Transfer Progress Bubbles ───────────────────────────────────── */
function handleFileProgress(ev, isUpload) {
  const id = ev.id || ev.name; // fallback to name
  const peer = isUpload ? ev.peer : ev.from;
  
  if (ev.done) {
    delete activeTransfers[id];
    if (activePeer === peer) {
      loadMsgs(peer);
    }
    return;
  }
  
  activeTransfers[id] = {
    name: ev.name,
    sent: isUpload ? ev.sent : ev.recv,
    total: ev.total,
    speedKBs: ev.speed_kbs || 0,
    done: ev.done,
    isUpload: isUpload,
    peer: peer
  };
  
  if (activePeer === peer) {
    // Re-render message area to update/insert the progress bar
    renderActiveTransfersOnly();
  }
}

function renderActiveTransfersOnly() {
  const el = document.getElementById('messages');
  // Remove existing progress bubbles
  document.querySelectorAll('.xfer-row').forEach(node => node.remove());
  
  // Append active ones
  Object.keys(activeTransfers).forEach(id => {
    const x = activeTransfers[id];
    if (x.peer === activePeer) {
      el.appendChild(buildTransferBubble(id, x));
    }
  });
  el.scrollTop = el.scrollHeight;
}

function buildTransferBubble(id, x) {
  const row = document.createElement('div');
  row.className = `msg-row xfer-row ${x.isUpload ? 'sent' : 'recv'}`;
  
  const pct = Math.min(100, Math.round((x.sent / x.total) * 100)) || 0;
  const speedStr = x.speedKBs > 1024 
    ? `${(x.speedKBs/1024).toFixed(1)} MB/s` 
    : `${Math.round(x.speedKBs)} KB/s`;
  
  const sentSizeStr  = (x.sent / (1024*1024)).toFixed(1);
  const totalSizeStr = (x.total / (1024*1024)).toFixed(1);

  row.innerHTML = `
    <div class="xfer-bubble">
      <div class="xfer-name">${x.isUpload ? '📤 Sending' : '📥 Receiving'}: ${esc(x.name)}</div>
      <div class="xfer-bar-wrap">
        <div class="xfer-bar" style="width: ${pct}%"></div>
      </div>
      <div class="xfer-info">
        <span>${pct}% (${sentSizeStr} / ${totalSizeStr} MB)</span>
        <span>${speedStr}</span>
      </div>
    </div>
  `;
  return row;
}

/* ── Send & Voice Recording ────────────────────────────────────────────── */
let mediaRecorder = null;
let audioChunks = [];
let recordingTimerInterval = null;
let recordingStartTime = 0;

async function sendMsg() {
  if (!activePeer) return;
  const inp  = document.getElementById('msg-input');
  let body = inp.value.trim();
  if (!body) return;
  
  // Format body with reply prefix if replying to a message
  if (replyToID) {
    body = `[reply:${replyToID}:${btoa(unescape(encodeURIComponent(replyToBody)))}]${body}`;
    cancelReply();
  }

  inp.value = '';
  handleInputEvent();
  try {
    await post('/api/send', { peer_id: activePeer, body });
  } catch (e) { logBar('Send failed: ' + e, 'err'); }
}

function inputKey(e) {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendMsg(); }
}

function autoResize(el) {
  el.style.height = 'auto';
  el.style.height = Math.min(el.scrollHeight, 160) + 'px';
}

function handleInputEvent() {
  const inp = document.getElementById('msg-input');
  autoResize(inp);
  
  const hasText = inp.value.trim().length > 0;
  if (hasText) {
    document.getElementById('record-btn').style.display = 'none';
    document.getElementById('send-btn').style.display = 'flex';
  } else {
    document.getElementById('record-btn').style.display = 'flex';
    document.getElementById('send-btn').style.display = 'none';
  }
}

async function toggleRecord() {
  if (mediaRecorder && mediaRecorder.state === 'recording') {
    stopAndSendRecord();
  } else {
    await startRecord();
  }
}

async function startRecord() {
  if (!activePeer) { logBar('Select a contact first', 'err'); return; }
  
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    audioChunks = [];
    
    let options = {};
    if (MediaRecorder.isTypeSupported('audio/webm')) {
      options = { mimeType: 'audio/webm' };
    } else if (MediaRecorder.isTypeSupported('audio/ogg')) {
      options = { mimeType: 'audio/ogg' };
    } else if (MediaRecorder.isTypeSupported('audio/mp4')) {
      options = { mimeType: 'audio/mp4' };
    }

    mediaRecorder = new MediaRecorder(stream, options);
    mediaRecorder.isCancelled = false;
    
    mediaRecorder.ondataavailable = (event) => {
      if (event.data.size > 0) {
        audioChunks.push(event.data);
      }
    };

    mediaRecorder.onstop = async () => {
      stream.getTracks().forEach(track => track.stop());
      
      if (audioChunks.length === 0) return;
      
      if (!mediaRecorder.isCancelled) {
        const audioBlob = new Blob(audioChunks, { type: mediaRecorder.mimeType || 'audio/webm' });
        
        let ext = 'webm';
        if (mediaRecorder.mimeType) {
          if (mediaRecorder.mimeType.includes('ogg')) ext = 'ogg';
          else if (mediaRecorder.mimeType.includes('mp4')) ext = 'm4a';
          else if (mediaRecorder.mimeType.includes('wav')) ext = 'wav';
        }
        
        const file = new File([audioBlob], `voice_${Date.now()}.${ext}`, { type: audioBlob.type });
        await uploadVoiceFile(file);
      }
      
      resetRecordUI();
    };

    document.getElementById('attach-btn').style.display = 'none';
    document.getElementById('msg-input').style.display = 'none';
    document.getElementById('record-status').style.display = 'flex';
    document.getElementById('record-cancel-btn').style.display = 'block';
    
    const recordBtn = document.getElementById('record-btn');
    recordBtn.classList.add('recording');
    recordBtn.innerHTML = `
      <svg width="18" height="18" fill="none" stroke="currentColor" stroke-width="2.5" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" d="M4.5 12.75l6 6 9-13.5" />
      </svg>
    `;

    mediaRecorder.start();
    
    recordingStartTime = Date.now();
    updateRecordTimer();
    recordingTimerInterval = setInterval(updateRecordTimer, 1000);
    
    logBar('Recording voice message...', 'ok');
  } catch (err) {
    console.error('Failed to start recording:', err);
    logBar('Microphone access denied or not available', 'err');
  }
}

async function uploadVoiceFile(file) {
  const fd = new FormData();
  fd.append('file', file);
  logBar(`Sending voice message…`);
  try {
    const r = await fetch(`/api/upload?peer=${encodeURIComponent(activePeer)}`, { method: 'POST', body: fd });
    const d = await r.json();
    if (d.ok) logBar(`Voice message sent`, 'ok');
    else      logBar(`Failed to send voice message: ${d.error}`, 'err');
  } catch(e) { 
    logBar('Upload error: ' + e, 'err'); 
  }
}

function updateRecordTimer() {
  const elapsed = Math.floor((Date.now() - recordingStartTime) / 1000);
  const m = String(Math.floor(elapsed / 60)).padStart(2, '0');
  const s = String(elapsed % 60).padStart(2, '0');
  document.getElementById('record-timer').textContent = `${m}:${s}`;
}

function stopAndSendRecord() {
  if (mediaRecorder && mediaRecorder.state === 'recording') {
    mediaRecorder.isCancelled = false;
    mediaRecorder.stop();
  }
}

function cancelRecord() {
  if (mediaRecorder && mediaRecorder.state === 'recording') {
    mediaRecorder.isCancelled = true;
    mediaRecorder.stop();
    logBar('Recording cancelled', 'ok');
  }
}

function resetRecordUI() {
  if (recordingTimerInterval) {
    clearInterval(recordingTimerInterval);
    recordingTimerInterval = null;
  }
  
  document.getElementById('attach-btn').style.display = 'block';
  document.getElementById('msg-input').style.display = 'block';
  document.getElementById('record-status').style.display = 'none';
  document.getElementById('record-cancel-btn').style.display = 'none';
  
  const recordBtn = document.getElementById('record-btn');
  recordBtn.classList.remove('recording');
  recordBtn.innerHTML = `
    <svg width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">
      <path stroke-linecap="round" stroke-linejoin="round" d="M12 18.75a6 6 0 0 0 6-6v-1.5m-6 7.5a6 6 0 0 1-6-6v-1.5m6 7.5v3.75m-3.75 0h7.5M12 15.75a3 3 0 0 1-3-3V4.5a3 3 0 1 1 6 0v8.25a3 3 0 0 1-3 3Z" />
    </svg>
  `;
  
  document.getElementById('file-input').value = '';
  handleInputEvent();
}

/* ── File Upload ───────────────────────────────────────────────────────── */
function pickFile(accept) {
  if (!activePeer) { logBar('Select a contact first', 'err'); return; }
  const fi = document.getElementById('file-input');
  fi.accept = accept;
  fi.click();
}

async function uploadFile() {
  const fi = document.getElementById('file-input');
  if (!fi.files.length || !activePeer) return;
  const name = fi.files[0].name;
  const size  = fi.files[0].size;
  
  const fd = new FormData();
  fd.append('file', fi.files[0]);
  logBar(`Uploading ${name} (${(size/(1024*1024)).toFixed(1)} MB)…`);
  try {
    const r = await fetch(`/api/upload?peer=${encodeURIComponent(activePeer)}`, {method:'POST', body:fd});
    const d = await r.json();
    if (d.ok) logBar(`Sent request: ${d.name}`, 'ok');
    else      logBar(`Upload failed: ${d.error}`, 'err');
  } catch(e) { logBar('Upload error: ' + e, 'err'); }
  fi.value = '';
}

function dlFile(url, name) {
  const a = document.createElement('a');
  a.href = url + '?download=1'; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
}

/* ── Contact Actions ───────────────────────────────────────────────────── */
function openAddContact() {
  document.getElementById('ac-onion').value = '';
  document.getElementById('ac-nick').value  = '';
  document.getElementById('modal-add').classList.add('open');
  setTimeout(() => document.getElementById('ac-onion').focus(), 60);
}

function closeHeaderDropdown() {
  const menu = document.getElementById('header-dropdown');
  if (menu) {
    menu.classList.remove('show');
  }
}

function openEditContact() {
  if (!activePeer) return;
  closeHeaderDropdown();
  const c = contacts.find(item => item.id === activePeer) || { id: activePeer, nickname: '', libp2p_id: '' };
  document.getElementById('ec-onion').value = c.id;
  document.getElementById('ec-nick').value  = c.nickname || '';
  document.getElementById('ec-peerid').value = c.libp2p_id || '';
  
  // Reset Onion field readonly state and show Edit button
  const onionEl = document.getElementById('ec-onion');
  onionEl.setAttribute('readonly', 'true');
  onionEl.style.opacity = '0.6';
  onionEl.style.cursor = 'not-allowed';
  const unlockBtn = document.getElementById('ec-onion-unlock');
  if (unlockBtn) unlockBtn.style.display = 'inline-block';

  document.getElementById('modal-edit').classList.add('open');
  setTimeout(() => document.getElementById('ec-nick').focus(), 60);
}

function openModal(id) { document.getElementById(id).classList.add('open'); }
function closeModal(id) { 
  document.getElementById(id).classList.remove('open'); 
  // Clear inputs on close
  if (id === 'modal-add') {
    document.getElementById('ac-onion').value = '';
    document.getElementById('ac-nick').value = '';
    document.getElementById('ac-peerid').value = '';
  }
}

async function addContact() {
  const id     = document.getElementById('ac-onion').value.trim();
  const nick   = document.getElementById('ac-nick').value.trim();
  const peerid = document.getElementById('ac-peerid').value.trim();
  if (!id) { document.getElementById('ac-onion').focus(); return; }
  await post('/api/contacts', { id, nickname: nick, libp2p_id: peerid });
  closeModal('modal-add');
  logBar('Contact added: ' + (nick || id.slice(0,14)), 'ok');
}

async function saveEditContact() {
  const id     = document.getElementById('ec-onion').value;
  const nick   = document.getElementById('ec-nick').value.trim();
  const peerid = document.getElementById('ec-peerid').value.trim();
  await post('/api/contacts', { id, nickname: nick, libp2p_id: peerid });
  closeModal('modal-edit');
  logBar('Contact updated: ' + (nick || id.slice(0,14)), 'ok');
  
  // Refresh active header display name
  document.getElementById('chat-peer-name').textContent = nick || id.slice(0, 20) + '…';
  document.getElementById('chat-ava').textContent = (nick || 'K')[0].toUpperCase();
}

async function resolveOnionFromDHT(peerIdInputId, onionInputId) {
  const peerID = document.getElementById(peerIdInputId).value.trim();
  if (!peerID) {
    alert("Please enter a Libp2p Peer ID first");
    return;
  }
  
  logBar("Resolving onion address from DHT...", "info");
  try {
    const resp = await fetch(`/api/dht/resolve?peer_id=${encodeURIComponent(peerID)}`);
    const data = await resp.json();
    if (resp.ok && data.onion) {
      document.getElementById(onionInputId).value = data.onion;
      if (onionInputId === 'ec-onion') {
        const onionEl = document.getElementById('ec-onion');
        onionEl.removeAttribute('readonly');
        onionEl.style.opacity = '1';
        onionEl.style.cursor = 'text';
        const unlockBtn = document.getElementById('ec-onion-unlock');
        if (unlockBtn) unlockBtn.style.display = 'none';
      }
      logBar("Resolved Onion Address from DHT successfully!", "ok");
    } else {
      const errMsg = data.error || "Peer not found or resolved address does not contain Onion endpoint";
      alert("DHT Resolution failed: " + errMsg);
      logBar("DHT Resolution failed", "err");
    }
  } catch (err) {
    alert("Error: " + err.message);
    logBar("DHT Resolution failed", "err");
  }
}

async function deleteContact() {
  closeHeaderDropdown();
  if (!activePeer || !confirm('Remove this contact?')) return;
  await del(`/api/contacts?id=${encodeURIComponent(activePeer)}`);
  activePeer = null;
  document.getElementById('chat-panel').style.display = 'none';
  document.getElementById('no-chat').style.display    = 'flex';
}

async function clearChat() {
  closeHeaderDropdown();
  if (!activePeer || !confirm('Clear all messages?')) return;
  await del(`/api/messages?peer=${encodeURIComponent(activePeer)}`);
}

async function connectPeer() {
  closeHeaderDropdown();
  if (!activePeer) return;
  await post('/api/connect', { peer_id: activePeer });
  logBar('Connecting to ' + activePeer.slice(0, 16) + '…');
}

/* ── Sidebar (mobile) ──────────────────────────────────────────────────── */
function showSidebar() { document.getElementById('sidebar').classList.add('open'); }

/* ── Utils ─────────────────────────────────────────────────────────────── */
function copyToClipboard(text) {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    return navigator.clipboard.writeText(text);
  }
  const textArea = document.createElement("textarea");
  textArea.value = text;
  textArea.style.top = "0";
  textArea.style.left = "0";
  textArea.style.position = "fixed";
  textArea.style.opacity = "0";
  document.body.appendChild(textArea);
  textArea.focus();
  textArea.select();
  try {
    const successful = document.execCommand('copy');
    document.body.removeChild(textArea);
    if (successful) {
      return Promise.resolve();
    }
    return Promise.reject(new Error('Copy command failed'));
  } catch (err) {
    document.body.removeChild(textArea);
    return Promise.reject(err);
  }
}

function copyOnion() {
  if (!myOnion) {
    logBar('Onion address not ready yet', 'err');
    return;
  }
  copyToClipboard(myOnion)
    .then(() => logBar('Onion Address copied!', 'ok'))
    .catch(() => logBar('Failed to copy onion address', 'err'));
}

function copyPeerID() {
  if (!myPeerID) {
    logBar('Peer ID not ready yet', 'err');
    return;
  }
  copyToClipboard(myPeerID)
    .then(() => logBar('Libp2p Peer ID copied!', 'ok'))
    .catch(() => logBar('Failed to copy peer ID', 'err'));
}

function logBar(msg, cls) {
  const el = document.getElementById('log-bar');
  el.textContent = msg;
  el.className   = cls || '';
}

function esc(s) {
  return String(s)
    .replace(/&/g,'&amp;').replace(/</g,'&lt;')
    .replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

/* ── Fetch helpers ─────────────────────────────────────────────────────── */
async function get(url) {
  const r = await fetch(url);
  return r.json();
}
async function post(url, body) {
  const r = await fetch(url, {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)});
  return r.json();
}
async function del(url) {
  const r = await fetch(url, {method:'DELETE'});
  return r.json();
}

/* ── Menu & Bottom Sheet Actions ─────────────────────────────────────────── */
function toggleHeaderDropdown(e) {
  e.stopPropagation();
  const menu = document.getElementById('header-dropdown');
  if (menu) {
    menu.classList.toggle('show');
  }
}

function openAttachmentSheet() {
  if (!activePeer) {
    logBar('Select a contact first', 'err');
    return;
  }
  document.getElementById('sheet-attachment').classList.add('open');
}

function closeAttachmentSheet() {
  document.getElementById('sheet-attachment').classList.remove('open');
}

function triggerAttachment(mimeType) {
  closeAttachmentSheet();
  pickFile(mimeType);
}

/* ── WebRTC Calling System (Standard RTCPeerConnection) ──────── */
let callActive = false;
let iceCandidatesQueue = [];

const rtcConfig = {
  iceServers: [
    { urls: 'stun:stun.l.google.com:19302' },
    { urls: 'stun:stun1.l.google.com:19302' },
    { urls: 'stun:stun2.l.google.com:19302' }
  ]
};

function sendCallSignaling(peer, data) {
  post('/api/send', { peer_id: peer, body: `[call-sig:${JSON.stringify(data)}]` })
    .catch(err => console.error("Signaling send error:", err));
}

function createPeerConnection(type) {
  if (peerConnection) {
    try {
      peerConnection.close();
    } catch(e){}
  }
  
  peerConnection = new RTCPeerConnection(rtcConfig);
  
  // Add local stream tracks to connection
  if (localStream) {
    localStream.getTracks().forEach(track => {
      peerConnection.addTrack(track, localStream);
    });
  }
  
  // Handle ICE candidates
  peerConnection.onicecandidate = event => {
    if (event.candidate && callPeer) {
      sendCallSignaling(callPeer, { type: 'candidate', candidate: event.candidate });
    }
  };
  
  // Handle remote track
  peerConnection.ontrack = event => {
    console.log("Remote track received:", event.streams[0]);
    const remoteVideo = document.getElementById('remote-video');
    if (remoteVideo) {
      remoteVideo.srcObject = event.streams[0];
      remoteVideo.load();
      remoteVideo.play().catch(e => console.warn("Remote video play:", e));
    }
  };
  
  peerConnection.onconnectionstatechange = () => {
    console.log("WebRTC Connection State:", peerConnection.connectionState);
    if (peerConnection.connectionState === 'connected') {
      logBar("WebRTC Direct Call Connected!", "ok");
    } else if (peerConnection.connectionState === 'failed' || peerConnection.connectionState === 'disconnected') {
      logBar("WebRTC Call Disconnected", "err");
    }
  };
}

async function initiateCall(type) {
  if (!activePeer) {
    logBar('Select a contact to call', 'err');
    return;
  }
  callType = type;
  callPeer = activePeer;
  callActive = false;
  iceCandidatesQueue = [];
  
  // Show calling overlay
  const contact = contacts.find(c => c.id === callPeer) || { id: callPeer, nickname: '' };
  const displayName = contact.nickname || callPeer.slice(0, 16);
  
  document.getElementById('active-call-name').textContent = `Calling ${displayName}...`;
  document.getElementById('active-call-avatar').textContent = (contact.nickname || 'K')[0].toUpperCase();
  document.getElementById('call-timer').textContent = "00:00";
  document.getElementById('call-screen').classList.add('active');
  
  // Reset buttons
  document.getElementById('btn-toggle-mic').classList.remove('muted');
  document.getElementById('btn-toggle-cam').classList.remove('muted');
  
  if (type === 'video') {
    document.getElementById('video-container').style.display = 'flex';
    document.getElementById('audio-call-display').style.display = 'none';
  } else {
    document.getElementById('video-container').style.display = 'none';
    document.getElementById('audio-call-display').style.display = 'flex';
  }

  const constraints = {
    audio: true,
    video: type === 'video' ? {
      width: { ideal: 640 },
      height: { ideal: 480 },
      frameRate: { ideal: 15 }
    } : false
  };

  try {
    logBar('Acquiring camera and microphone...', 'ok');
    localStream = await navigator.mediaDevices.getUserMedia(constraints);
    
    const localVideo = document.getElementById('local-video');
    if (localVideo) localVideo.srcObject = localStream;

    // Create RTCPeerConnection
    createPeerConnection(type);

    // Create SDP Offer
    const offer = await peerConnection.createOffer();
    await peerConnection.setLocalDescription(offer);

    sendCallSignaling(callPeer, { type: 'offer', callType: type, sdp: offer });
    logBar('Calling...', 'ok');
    
  } catch (err) {
    console.error('Call initialization failed:', err);
    logBar('Call failed: permissions denied or media unavailable', 'err');
    cleanupCall();
  }
}

async function handleCallSignaling(payload) {
  const fromPeer = payload.peer;
  let sig;
  try {
    sig = JSON.parse(payload.data);
  } catch (err) {
    console.error("Invalid signaling JSON:", err);
    return;
  }

  if (sig.type === 'offer') {
    if (callActive) {
      sendCallSignaling(fromPeer, { type: 'reject', reason: 'busy' });
      return;
    }
    callPeer = fromPeer;
    callType = sig.callType;
    incomingCallSignal = sig;
    iceCandidatesQueue = [];

    const contact = contacts.find(c => c.id === fromPeer) || { id: fromPeer, nickname: '' };
    const displayName = contact.nickname || fromPeer.slice(0, 16);

    document.getElementById('inc-call-avatar').textContent = (contact.nickname || 'K')[0].toUpperCase();
    document.getElementById('inc-call-title').textContent = `Incoming ${sig.callType === 'video' ? 'Video' : 'Audio'} Call`;
    document.getElementById('inc-call-peer-id').textContent = displayName;
    document.getElementById('modal-incoming-call').classList.add('open');
    
    sendCallSignaling(fromPeer, { type: 'ring' });
    
  } else if (sig.type === 'ring') {
    if (callPeer === fromPeer) {
      document.getElementById('active-call-name').textContent = `Ringing...`;
    }
  } else if (sig.type === 'reject') {
    if (callPeer === fromPeer) {
      logBar(sig.reason === 'busy' ? 'Peer is busy' : 'Call declined', 'err');
      cleanupCall();
    }
  } else if (sig.type === 'answer') {
    if (callPeer === fromPeer && peerConnection) {
      callActive = true;
      startCallTimer();
      document.getElementById('active-call-name').textContent = `Call Active`;
      
      try {
        await peerConnection.setRemoteDescription(new RTCSessionDescription(sig.sdp));
      } catch(e) {
        console.error("Error setting remote description from answer:", e);
      }
    }
  } else if (sig.type === 'candidate') {
    if (callPeer === fromPeer) {
      if (peerConnection && peerConnection.remoteDescription) {
        try {
          await peerConnection.addIceCandidate(new RTCIceCandidate(sig.candidate));
        } catch(e) {
          console.error("Error adding Ice Candidate:", e);
        }
      } else {
        iceCandidatesQueue.push(sig.candidate);
      }
    }
  } else if (sig.type === 'switch_video') {
    if (callPeer === fromPeer) {
      if (sig.enabled) {
        callType = 'video';
        document.getElementById('video-container').style.display = 'flex';
        document.getElementById('audio-call-display').style.display = 'none';
        
        // Auto acquire camera to add video track to peer connection dynamically
        const videoTrack = localStream ? localStream.getVideoTracks()[0] : null;
        if (!videoTrack) {
          try {
            const videoStream = await navigator.mediaDevices.getUserMedia({
              video: { width: { ideal: 640 }, height: { ideal: 480 }, frameRate: { ideal: 15 } }
            });
            const vt = videoStream.getVideoTracks()[0];
            if (localStream) {
              localStream.addTrack(vt);
              const localVideo = document.getElementById('local-video');
              if (localVideo) localVideo.srcObject = localStream;
              
              if (peerConnection) {
                peerConnection.addTrack(vt, localStream);
                
                // Renegotiate connection
                const offer = await peerConnection.createOffer();
                await peerConnection.setLocalDescription(offer);
                sendCallSignaling(callPeer, { type: 'offer_update', sdp: offer });
              }
              document.getElementById('btn-toggle-cam').classList.remove('muted');
            }
          } catch(e) {
            console.error("Could not auto-start camera:", e);
          }
        }
      } else {
        document.getElementById('video-container').style.display = 'none';
        document.getElementById('audio-call-display').style.display = 'flex';
      }
    }
  } else if (sig.type === 'offer_update') {
    if (callPeer === fromPeer && peerConnection) {
      try {
        await peerConnection.setRemoteDescription(new RTCSessionDescription(sig.sdp));
        const answer = await peerConnection.createAnswer();
        await peerConnection.setLocalDescription(answer);
        sendCallSignaling(callPeer, { type: 'answer_update', sdp: answer });
      } catch(e) {
        console.error("Error handling dynamic offer update:", e);
      }
    }
  } else if (sig.type === 'answer_update') {
    if (callPeer === fromPeer && peerConnection) {
      try {
        await peerConnection.setRemoteDescription(new RTCSessionDescription(sig.sdp));
      } catch(e) {
        console.error("Error setting answer update:", e);
      }
    }
  } else if (sig.type === 'hangup') {
    if (callPeer === fromPeer) {
      logBar('Call ended by peer', 'ok');
      cleanupCall();
    }
  }
}

async function acceptCall() {
  document.getElementById('modal-incoming-call').classList.remove('open');
  if (!incomingCallSignal) return;

  const contact = contacts.find(c => c.id === callPeer) || { id: callPeer, nickname: '' };
  const displayName = contact.nickname || callPeer.slice(0, 16);

  document.getElementById('active-call-name').textContent = `Connecting...`;
  document.getElementById('active-call-avatar').textContent = (contact.nickname || 'K')[0].toUpperCase();
  document.getElementById('call-timer').textContent = "00:00";
  document.getElementById('call-screen').classList.add('active');

  // Reset buttons
  document.getElementById('btn-toggle-mic').classList.remove('muted');
  document.getElementById('btn-toggle-cam').classList.remove('muted');

  if (callType === 'video') {
    document.getElementById('video-container').style.display = 'flex';
    document.getElementById('audio-call-display').style.display = 'none';
  } else {
    document.getElementById('video-container').style.display = 'none';
    document.getElementById('audio-call-display').style.display = 'flex';
  }

  const constraints = {
    audio: true,
    video: callType === 'video' ? {
      width: { ideal: 640 },
      height: { ideal: 480 },
      frameRate: { ideal: 15 }
    } : false
  };

  try {
    localStream = await navigator.mediaDevices.getUserMedia(constraints);

    const localVideo = document.getElementById('local-video');
    if (localVideo) localVideo.srcObject = localStream;

    // Create RTCPeerConnection
    createPeerConnection(callType);

    // Set remote description (SDP offer)
    await peerConnection.setRemoteDescription(new RTCSessionDescription(incomingCallSignal.sdp));

    // Create SDP Answer
    const answer = await peerConnection.createAnswer();
    await peerConnection.setLocalDescription(answer);

    callActive = true;
    sendCallSignaling(callPeer, { type: 'answer', sdp: answer });
    startCallTimer();
    document.getElementById('active-call-name').textContent = `Call Active`;

    // Process queued candidates
    while (iceCandidatesQueue.length > 0) {
      const candidate = iceCandidatesQueue.shift();
      try {
        await peerConnection.addIceCandidate(new RTCIceCandidate(candidate));
      } catch(e) {
        console.error("Error adding queued candidate:", e);
      }
    }

  } catch (err) {
    console.error("Accept call failed:", err);
    logBar("Failed to connect call", "err");
    sendCallSignaling(callPeer, { type: 'reject', reason: 'failed' });
    cleanupCall();
  }
}

function rejectCall() {
  document.getElementById('modal-incoming-call').classList.remove('open');
  if (callPeer) {
    sendCallSignaling(callPeer, { type: 'reject', reason: 'declined' });
  }
  cleanupCall();
}

function hangupCall() {
  if (callPeer) {
    sendCallSignaling(callPeer, { type: 'hangup' });
  }
  cleanupCall();
}

function cleanupCall() {
  if (callTimerInterval) {
    clearInterval(callTimerInterval);
    callTimerInterval = null;
  }
  callStartTime = null;
  callActive = false;

  if (peerConnection) {
    try {
      peerConnection.close();
    } catch(e){}
    peerConnection = null;
  }

  if (localStream) {
    localStream.getTracks().forEach(track => track.stop());
    localStream = null;
  }

  const localVideo = document.getElementById('local-video');
  const remoteVideo = document.getElementById('remote-video');
  if (localVideo) localVideo.srcObject = null;
  if (remoteVideo) remoteVideo.srcObject = null;

  callPeer = null;
  callType = null;
  incomingCallSignal = null;
  iceCandidatesQueue = [];

  document.getElementById('call-screen').classList.remove('active');
  document.getElementById('modal-incoming-call').classList.remove('open');
}

function startCallTimer() {
  callStartTime = Date.now();
  if (callTimerInterval) clearInterval(callTimerInterval);
  callTimerInterval = setInterval(() => {
    const elapsed = Math.floor((Date.now() - callStartTime) / 1000);
    const mm = String(Math.floor(elapsed / 60)).padStart(2, '0');
    const ss = String(elapsed % 60).padStart(2, '0');
    document.getElementById('call-timer').textContent = `${mm}:${ss}`;
  }, 1000);
}

function toggleMic() {
  if (localStream) {
    const audioTrack = localStream.getAudioTracks()[0];
    if (audioTrack) {
      audioTrack.enabled = !audioTrack.enabled;
      document.getElementById('btn-toggle-mic').classList.toggle('muted', !audioTrack.enabled);
      logBar(audioTrack.enabled ? 'Microphone unmuted' : 'Microphone muted', 'ok');
    }
  }
}

async function toggleCam() {
  if (!localStream) return;
  
  if (callActive && callType === 'audio') {
    try {
      logBar('Enabling camera...', 'ok');
      const videoStream = await navigator.mediaDevices.getUserMedia({
        video: { width: { ideal: 640 }, height: { ideal: 480 }, frameRate: { ideal: 15 } }
      });
      const videoTrack = videoStream.getVideoTracks()[0];
      localStream.addTrack(videoTrack);
      
      const localVideo = document.getElementById('local-video');
      if (localVideo) localVideo.srcObject = localStream;
      
      callType = 'video';
      
      document.getElementById('video-container').style.display = 'flex';
      document.getElementById('audio-call-display').style.display = 'none';
      document.getElementById('btn-toggle-cam').classList.remove('muted');
      
      if (peerConnection) {
        peerConnection.addTrack(videoTrack, localStream);
        
        // Renegotiate connection
        const offer = await peerConnection.createOffer();
        await peerConnection.setLocalDescription(offer);
        sendCallSignaling(callPeer, { type: 'offer_update', sdp: offer });
      }
      
      sendCallSignaling(callPeer, { type: 'switch_video', enabled: true });
      logBar('Switched to Video call', 'ok');
    } catch (err) {
      console.error("Camera acquisition failed:", err);
      logBar("Failed to acquire camera", "err");
    }
    return;
  }
  
  const videoTrack = localStream.getVideoTracks()[0];
  if (videoTrack) {
    videoTrack.enabled = !videoTrack.enabled;
    document.getElementById('btn-toggle-cam').classList.toggle('muted', !videoTrack.enabled);
    logBar(videoTrack.enabled ? 'Camera enabled' : 'Camera disabled', 'ok');
    if (callActive) {
      sendCallSignaling(callPeer, { type: 'switch_video', enabled: videoTrack.enabled });
      if (videoTrack.enabled) {
        document.getElementById('video-container').style.display = 'flex';
        document.getElementById('audio-call-display').style.display = 'none';
      } else {
        document.getElementById('video-container').style.display = 'none';
        document.getElementById('audio-call-display').style.display = 'flex';
      }
    }
  } else if (callActive) {
    try {
      logBar('Enabling camera...', 'ok');
      const videoStream = await navigator.mediaDevices.getUserMedia({
        video: { width: { ideal: 640 }, height: { ideal: 480 }, frameRate: { ideal: 15 } }
      });
      const vt = videoStream.getVideoTracks()[0];
      localStream.addTrack(vt);
      
      const localVideo = document.getElementById('local-video');
      if (localVideo) localVideo.srcObject = localStream;
      
      if (peerConnection) {
        peerConnection.addTrack(vt, localStream);
        
        const offer = await peerConnection.createOffer();
        await peerConnection.setLocalDescription(offer);
        sendCallSignaling(callPeer, { type: 'offer_update', sdp: offer });
      }
      
      document.getElementById('video-container').style.display = 'flex';
      document.getElementById('audio-call-display').style.display = 'none';
      document.getElementById('btn-toggle-cam').classList.remove('muted');
      
      sendCallSignaling(callPeer, { type: 'switch_video', enabled: true });
    } catch (err) {
      console.error(err);
    }
  }
}

/* ── Settings & Advanced Context Menu Functions ───────────────────────── */

function openSettings() {
  document.getElementById('setting-debug').checked = debugMode;
  document.getElementById('setting-hide-sidebar').checked = hideSidebarSetting;
  document.getElementById('setting-font-size').value = chatFontSize;
  openModal('modal-settings');
}

function toggleDebugMode(checked) {
  debugMode = checked;
  localStorage.setItem('debugMode', checked);
  renderMessages(currentMessages, true);
}

function toggleSidebarSetting(checked) {
  hideSidebarSetting = checked;
  localStorage.setItem('hideSidebarSetting', checked);
  applySidebarState();
}

function toggleSidebar() {
  const isCollapsed = document.body.classList.toggle('sidebar-collapsed');
  localStorage.setItem('sidebarCollapsed', isCollapsed);
}

function applySidebarState() {
  if (hideSidebarSetting || localStorage.getItem('sidebarCollapsed') === 'true') {
    document.body.classList.add('sidebar-collapsed');
  } else {
    document.body.classList.remove('sidebar-collapsed');
  }
}

function changeChatFontSize(val) {
  chatFontSize = val;
  localStorage.setItem('chatFontSize', val);
  document.documentElement.style.setProperty('--chat-font-size', val);
}

// Context Menu triggers
function showContextMenu(e, m) {
  selectedMsgID = m.id;
  selectedMsgBody = m.body;
  
  const menu = document.getElementById('bubble-context-menu');
  menu.style.display = 'block';
  
  // Update Star action text
  const starBtn = document.getElementById('ctx-star-btn');
  if (m.starred) {
    starBtn.innerHTML = '<span>★</span> Unstar Message';
  } else {
    starBtn.innerHTML = '<span>⭐️</span> Star Message';
  }
  
  let x = e.clientX;
  let y = e.clientY;
  
  const menuWidth = 160;
  const menuHeight = 200;
  if (x + menuWidth > window.innerWidth) x = window.innerWidth - menuWidth - 10;
  if (y + menuHeight > window.innerHeight) y = window.innerHeight - menuHeight - 10;
  
  menu.style.left = `${x}px`;
  menu.style.top = `${y}px`;
  
  const closeMenu = () => {
    menu.style.display = 'none';
    document.removeEventListener('click', closeMenu);
  };
  setTimeout(() => document.addEventListener('click', closeMenu), 50);
}

function scrollToMessage(id) {
  const target = document.querySelector(`.bubble[data-id="${id}"]`);
  if (target) {
    target.scrollIntoView({ behavior: 'smooth', block: 'center' });
    target.classList.add('flash-highlight');
    setTimeout(() => target.classList.remove('flash-highlight'), 2000);
  }
}

function triggerReply() {
  replyToID = selectedMsgID;
  replyToBody = selectedMsgBody;
  
  const bar = document.getElementById('reply-preview-bar');
  const txt = document.getElementById('reply-preview-text');
  
  let cleanText = replyToBody || '';
  if (cleanText.startsWith('[media-sent:')) cleanText = '🖼️ Media File';
  else if (cleanText.startsWith('[media-recv:')) cleanText = '🖼️ Media File';
  
  txt.innerText = cleanText;
  bar.style.display = 'flex';
  document.getElementById('msg-input').focus();
}

function cancelReply() {
  replyToID = null;
  replyToBody = null;
  document.getElementById('reply-preview-bar').style.display = 'none';
}

async function triggerStar() {
  if (!activePeer || !selectedMsgID) return;
  
  // Find current message starred state
  const m = currentMessages.find(msg => msg.id === selectedMsgID);
  const nextStarred = m ? !m.starred : true;
  
  try {
    const res = await post('/api/messages/star', {
      peer_id: activePeer,
      msg_id: selectedMsgID,
      starred: nextStarred
    });
    if (res && res.ok) {
      logBar(nextStarred ? 'Message starred' : 'Message unstarred', 'ok');
    }
  } catch (e) {
    logBar('Failed to star message: ' + e, 'err');
  }
}

function triggerForward() {
  // Show list of contacts inside forwarding modal
  const listEl = document.getElementById('forward-contact-list');
  listEl.innerHTML = '';
  
  if (!contacts.length) {
    listEl.innerHTML = '<p style="text-align:center;padding:10px;color:var(--text-muted);">No contacts available to forward</p>';
  } else {
    contacts.forEach(c => {
      const item = document.createElement('div');
      item.className = 'selector-contact-item';
      
      const name = c.nickname || c.id.slice(0, 16) + '…';
      const initial = (c.nickname || 'K')[0].toUpperCase();
      
      item.innerHTML = `
        <div class="sel-ava">${initial}</div>
        <div style="flex:1;">
          <div style="font-weight:600;font-size:13.5px;">${esc(name)}</div>
          <div style="font-size:11px;color:var(--text-muted);">${c.id.slice(0, 24)}…</div>
        </div>
      `;
      item.onclick = async () => {
        closeModal('modal-forward');
        await forwardMessageTo(c.id, selectedMsgBody);
      };
      listEl.appendChild(item);
    });
  }
  openModal('modal-forward');
}

async function forwardMessageTo(peerID, body) {
  try {
    await post('/api/send', { peer_id: peerID, body });
    logBar('Message forwarded successfully', 'ok');
  } catch (e) {
    logBar('Forward failed: ' + e, 'err');
  }
}

function triggerShare() {
  if (!selectedMsgBody) return;
  
  let cleanText = selectedMsgBody || '';
  if (cleanText.startsWith('[media-sent:')) {
    const parts = cleanText.split(':');
    cleanText = window.location.origin + '/api/media/' + parts[1].replace(']', '');
  } else if (cleanText.startsWith('[media-recv:')) {
    const parts = cleanText.split(':');
    cleanText = window.location.origin + '/api/media/' + parts[1].replace(']', '');
  }

  if (navigator.share) {
    navigator.share({ text: cleanText }).catch(err => {
      console.log('Share error', err);
    });
  } else {
    navigator.clipboard.writeText(cleanText).then(() => {
      logBar('Message copied to clipboard', 'ok');
    }).catch(err => {
      logBar('Failed to copy: ' + err, 'err');
    });
  }
}

async function triggerDelete() {
  if (!activePeer || !selectedMsgID) return;
  if (!confirm('Are you sure you want to delete this message?')) return;
  
  try {
    const res = await del(`/api/messages?peer=${encodeURIComponent(activePeer)}&id=${encodeURIComponent(selectedMsgID)}`);
    if (res && res.ok) {
      logBar('Message deleted', 'ok');
    }
  } catch (e) {
    logBar('Delete failed: ' + e, 'err');
  }
}

// Database actions
async function clearAllDatabase() {
  if (!confirm('CRITICAL WARNING: This will delete ALL chat histories and ALL contacts. Are you absolutely sure?')) return;
  try {
    const res = await post('/api/db/clear', {});
    if (res && res.ok) {
      logBar('All database data cleared', 'ok');
      closeModal('modal-settings');
      location.reload();
    }
  } catch (e) {
    logBar('Database clear failed: ' + e, 'err');
  }
}

async function compactDatabase() {
  logBar('Compacting database in background...', 'ok');
  try {
    const res = await post('/api/db/compact', {});
    if (res && res.ok) {
      logBar('Database compaction complete!', 'ok');
    }
  } catch (e) {
    logBar('Compaction failed: ' + e, 'err');
  }
}

function inlineFormat(text) {
  // 1. Inline code: `text`
  text = text.replace(/`([^`]+?)`/g, '<code class="msg-inline-code">$1</code>');
  
  // 2. Bold: *text* (matches if not containing asterisks inside)
  text = text.replace(/\*([^*]+?)\*/g, '<strong>$1</strong>');
  
  // 3. Italic: _text_
  text = text.replace(/_([^_]+?)_/g, '<em>$1</em>');
  
  // 4. Strikethrough: ~text~
  text = text.replace(/~([^~]+?)~/g, '<del>$1</del>');

  // 5. Link detection: matches http:// or https:// URLs, ignoring surrounding tags or quotes
  text = text.replace(/(?<!href=["']|src=["'])(https?:\/\/[^\s<]+)/g, '<a href="$1" target="_blank" rel="noopener noreferrer" style="color:var(--accent); text-decoration:underline;">$1</a>');
  
  return text;
}

function formatMessageBody(text) {
  // First escape HTML to prevent XSS
  let html = esc(text);
  
  // Parse Monospace blocks: ```text```
  html = html.replace(/```([\s\S]+?)```/g, (match, code) => {
    return `<pre class="msg-pre"><code>${code}</code></pre>`;
  });
  
  let lines = html.split('\n');
  let processedLines = [];
  let inList = null; // 'ul', 'ol', or null
  
  for (let line of lines) {
    // If the line contains pre/code from monospace block, keep it as-is
    if (line.includes('<pre class="msg-pre">') || line.includes('</pre>')) {
      if (inList) {
        processedLines.push(`</${inList}>`);
        inList = null;
      }
      processedLines.push(line);
      continue;
    }
    
    // 1. Bulleted list: * text or - text
    let bulletMatch = line.match(/^(\s*)([\*\-])\s+(.+)$/);
    if (bulletMatch) {
      if (inList !== 'ul') {
        if (inList) processedLines.push(`</${inList}>`);
        processedLines.push('<ul>');
        inList = 'ul';
      }
      let content = inlineFormat(bulletMatch[3]);
      processedLines.push(`<li>${content}</li>`);
      continue;
    }
    
    // 2. Numbered list: 1. text
    let numberMatch = line.match(/^(\s*)(\d+)\.\s+(.+)$/);
    if (numberMatch) {
      if (inList !== 'ol') {
        if (inList) processedLines.push(`</${inList}>`);
        processedLines.push('<ol>');
        inList = 'ol';
      }
      let content = inlineFormat(numberMatch[3]);
      processedLines.push(`<li>${content}</li>`);
      continue;
    }
    
    // 3. Quote: > text
    let quoteMatch = line.match(/^(\s*)&gt;\s+(.+)$/);
    if (quoteMatch) {
      if (inList) {
        processedLines.push(`</${inList}>`);
        inList = null;
      }
      let content = inlineFormat(quoteMatch[2]);
      processedLines.push(`<blockquote>${content}</blockquote>`);
      continue;
    }
    
    // Regular line
    if (inList) {
      processedLines.push(`</${inList}>`);
      inList = null;
    }
    
    processedLines.push(inlineFormat(line));
  }
  
  if (inList) {
    processedLines.push(`</${inList}>`);
  }
  
  // Join lines dynamically avoiding <br> for block container transitions
  let finalHtml = '';
  const isBlock = (l) => /<\/?(ul|ol|li|blockquote|pre)/.test(l);
  
  for (let i = 0; i < processedLines.length; i++) {
    let line = processedLines[i];
    if (i > 0) {
      let prev = processedLines[i-1];
      if (!isBlock(prev) && !isBlock(line)) {
        finalHtml += '<br>';
      }
    }
    finalHtml += line;
  }
  
  return finalHtml;
}

async function renderLinkPreview(url, container) {
  try {
    const response = await fetch(`/api/link-preview?url=${encodeURIComponent(url)}`);
    if (!response.ok) return;
    const data = await response.json();
    
    // 1. If it's a direct image link
    if (data.is_image) {
      container.className = 'image-preview-card';
      container.innerHTML = `<img src="${esc(data.url)}" alt="Image Preview" onclick="window.open('${esc(data.url)}', '_blank')">`;
      return;
    }

    // Safely extract hostname
    let displayHostname = '';
    try {
      displayHostname = new URL(data.url || url).hostname.replace(/^www\./i, '');
    } catch(e) {
      displayHostname = data.url || url;
    }
    
    // 2. If it's a YouTube video
    if (data.is_youtube && data.youtube_id) {
      container.className = 'yt-preview-container';
      container.innerHTML = `
        <div class="yt-player">
          <iframe src="https://www.youtube.com/embed/${esc(data.youtube_id)}" allowfullscreen></iframe>
        </div>
        <div class="lp-content">
          <div class="lp-header">
            <img class="lp-icon" src="${esc(data.icon || 'https://www.youtube.com/favicon.ico')}" alt="">
            <span>YouTube</span>
          </div>
          <a class="lp-title" href="${esc(data.url)}" target="_blank" rel="noopener noreferrer">${esc(data.title || 'YouTube Video')}</a>
          ${data.description ? `<p class="lp-desc">${esc(data.description)}</p>` : ''}
        </div>
      `;
      return;
    }
    
    // 3. General link preview card
    container.className = 'link-preview-card';
    container.setAttribute('href', data.url);
    container.setAttribute('target', '_blank');
    container.setAttribute('rel', 'noopener noreferrer');
    
    let imgHtml = '';
    if (data.image) {
      imgHtml = `<img class="lp-image" src="${esc(data.image)}" alt="Preview">`;
    }
    
    container.innerHTML = `
      ${imgHtml}
      <div class="lp-content">
        <div class="lp-header">
          ${data.icon ? `<img class="lp-icon" src="${esc(data.icon)}" alt="">` : ''}
          <span>${esc(displayHostname)}</span>
        </div>
        <div class="lp-title">${esc(data.title || 'Web Page')}</div>
        ${data.description ? `<div class="lp-desc">${esc(data.description)}</div>` : ''}
      </div>
    `;
  } catch (err) {
    // Silently ignore preview failures to keep chat working smoothly
  }
}
