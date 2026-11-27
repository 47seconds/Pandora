(() => {
  'use strict';
  const app = document.getElementById('app');
  const state = { user: null, timer: null, rid: 0 };
  // The server stamps this script's URL with a content version (?v=...); reuse it for images
  // so a CDN never serves art from an older release.
  const ver = document.currentScript ? new URL(document.currentScript.src, location.href).search : '';
  const asset = (name) => name + ver;

  // PR titles, descriptions, names and AI text are untrusted: escape everything dynamic.
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const $ = (id) => document.getElementById(id);

  const STATUS = {
    queued: ['Queued', 'blue'], ai_review: ['Reviewing', 'blue'], ai_passed: ['AI passed', 'green'],
    needs_human: ['Needs human', 'amber'], blocked: ['Blocked', 'red'], error: ['Error', 'red'],
    superseded: ['Superseded', 'grey'], rejected: ['Rejected', 'red'], merged: ['Merged', 'purple'], closed: ['Closed', 'grey'],
    draft: ['Draft', 'grey'], none: ['Not reviewed', 'grey'],
  };
  const badge = (k) => { const [l, c] = STATUS[k] || [k, 'grey']; return `<span class="badge b-${c}">${esc(l)}</span>`; };
  const prStatus = (p) => p.latest_run && p.latest_run.status === 'rejected' ? badge('rejected') : p.state === 'closed' ? badge(p.merged ? 'merged' : 'closed') : p.draft ? badge('draft') : badge(p.latest_run ? p.latest_run.status : 'none');

  const short = (sha) => esc((sha || '').slice(0, 7));
  const when = (iso) => iso ? new Date(iso).toLocaleString() : '';
  const ago = (iso) => {
    if (!iso) return '';
    const s = Math.max(0, (Date.now() - new Date(iso)) / 1000);
    if (s < 60) return 'just now';
    if (s < 3600) return Math.floor(s / 60) + 'm ago';
    if (s < 86400) return Math.floor(s / 3600) + 'h ago';
    return Math.floor(s / 86400) + 'd ago';
  };
  const hours = (h) => !h ? '–' : h < 48 ? h.toFixed(1) + ' h' : (h / 24).toFixed(1) + ' d';
  const prUpdated = (p) => p.gh_updated_at || p.updated_at;
  const isAdmin = (r) => r === 'admin' || r === 'superadmin';
  const roleLabel = (r) => r === 'superadmin' ? 'super-admin' : r;

  const theme = {
    get() { try { return localStorage.getItem('pandora_theme') || 'system'; } catch { return 'system'; } },
    set(v) {
      try { v === 'system' ? localStorage.removeItem('pandora_theme') : localStorage.setItem('pandora_theme', v); } catch {}
      if (v === 'light' || v === 'dark') document.documentElement.setAttribute('data-theme', v);
      else document.documentElement.removeAttribute('data-theme');
    },
  };

  async function api(path, opts = {}) {
    const method = opts.method || 'GET';
    const headers = {};
    if (opts.body) headers['Content-Type'] = 'application/json';
    if (method !== 'GET') headers['X-Pandora-CSRF'] = '1'; // required by the server for writes
    const res = await fetch(path, { method, headers, body: opts.body, credentials: 'same-origin' });
    let body = null;
    try { body = await res.json(); } catch {}
    if (res.status === 401) { state.user = null; stopRefresh(); renderLogin(); throw new Error('signed out'); }
    if (!res.ok) throw new Error((body && body.error) || 'Request failed (' + res.status + ')');
    return body;
  }

  function stopRefresh() { if (state.timer) { clearInterval(state.timer); state.timer = null; } }
  function startRefresh(fn) { stopRefresh(); state.timer = setInterval(() => { if (!document.hidden) fn(); }, 30000); }

  // ---------- chrome ----------
  function shell(active, inner) {
    const u = state.user, pending = u.role === 'pending', admin = isAdmin(u.role);
    const link = (h, key, label) => `<a href="${h}" class="${active === key ? 'active' : ''}">${label}</a>`;
    app.innerHTML = `
      <header>
        <a class="brand" href="#/"><img class="logo" src="${asset('icon-192.png')}" alt="">Pandora</a>
        <nav>
          ${pending ? '' : link('#/my', 'my', 'My pull requests') + link('#/reviews', 'reviews', 'Review pull requests')}
          ${link('#/settings', 'settings', 'Settings')}
          ${admin ? link('#/admin', 'admin', 'Admin') : ''}
        </nav>
        <span class="who">${u.avatar_url ? `<img class="avatar" src="${esc(u.avatar_url)}" alt="">` : ''}${esc(u.github_login)}</span>
        <button id="logout">Log out</button>
      </header>
      <main id="main">${inner}</main>`;
    $('logout').onclick = async () => { try { await api('/auth/logout', { method: 'POST' }); } catch {} state.user = null; stopRefresh(); renderLogin(); };
  }

  function showError(e) {
    const m = $('main');
    if (m && e.message !== 'signed out') m.insertAdjacentHTML('afterbegin', `<div class="error">${esc(e.message)}</div>`);
  }

  const LOGIN_ERRORS = {
    access_denied: 'GitHub sign-in was cancelled.',
    invalid_state: 'That sign-in link expired or did not start here. Please try again.',
    github_failed: 'GitHub sign-in failed. Please try again.',
    account_disabled: 'Your account has been disabled. Contact an admin.',
    server_error: 'Something went wrong on our side. Please try again.',
  };

  function renderLogin() {
    const code = new URLSearchParams(location.search).get('error');
    if (code) history.replaceState(null, '', location.pathname + location.hash);
    app.innerHTML = `
      <div class="login card">
        <img class="banner" src="${asset('banner.webp')}" width="1200" height="427" alt="Pandora: automate PR, test and deploy">
        <div class="pad">
          <div class="error">${esc(code ? LOGIN_ERRORS[code] || 'Sign-in failed.' : '')}</div>
          <a class="gh" href="/auth/github/login">Continue with GitHub</a>
          <p class="muted">New accounts need an admin's approval before they can see anything.</p>
        </div>
      </div>`;
  }

  // ---------- PR tables ----------
  function prTable(prs, { author = false, reviewers = false } = {}) {
    const cols = 3 + (author ? 1 : 0) + (reviewers ? 1 : 0);
    const rows = prs.length ? prs.map((p) => `
      <tr class="click" data-id="${p.id}">
        <td><a href="#/pr/${p.id}"><strong>${esc(p.title) || '(no title)'}</strong></a><br><span class="muted mono">${esc(p.repo)}#${p.number}</span></td>
        ${author ? `<td>${esc(p.author)}</td>` : ''}
        ${reviewers ? `<td><div class="chips">${p.reviewers.map((r) => `<span class="chip">${esc(r)}</span>`).join('') || '<span class="muted">–</span>'}</div></td>` : ''}
        <td>${prStatus(p)}</td>
        <td class="muted" title="${esc(when(prUpdated(p)))}">${esc(ago(prUpdated(p)))}</td>
      </tr>`).join('') : `<tr><td colspan="${cols}" class="empty">Nothing here yet.</td></tr>`;
    return `<div class="card"><table><thead><tr><th>Pull request</th>${author ? '<th>Author</th>' : ''}${reviewers ? '<th>Reviewers</th>' : ''}<th>Status</th><th>Updated</th></tr></thead><tbody id="prrows">${rows}</tbody></table></div>`;
  }

  function wireRows() {
    const tb = $('prrows');
    if (tb) tb.onclick = (e) => { const tr = e.target.closest('tr[data-id]'); if (tr && !e.target.closest('a')) location.hash = '#/pr/' + tr.dataset.id; };
  }

  // A list page: shell once, then (re)load only the table so filters keep their state.
  async function listPage(rid, key, title, url, opts, withFilter) {
    let filter = withFilter ? 'open' : '';
    shell(key, `<h1>${esc(title)}</h1>
      ${withFilter ? `<div class="toolbar"><select id="filter" aria-label="State">
        <option value="open">Open</option><option value="merged">Merged</option><option value="closed">Closed</option><option value="">All</option></select>
        <span class="grow"></span></div>` : ''}<div id="listbox"></div>`);
    const load = async () => {
      try {
        const prs = await api(url + (filter ? '?state=' + filter : ''));
        if (rid !== state.rid) return;
        $('listbox').innerHTML = prTable(prs, opts);
        wireRows();
      } catch (e) { if (rid === state.rid) showError(e); }
    };
    if (withFilter) { $('filter').onchange = (e) => { filter = e.target.value; load(); }; }
    await load();
    startRefresh(load);
  }

  // ---------- PR detail ----------
  async function renderPR(rid, id) {
    stopRefresh();
    shell('', '<div class="muted">Loading…</div>');
    let data;
    try { data = await api('/api/v1/prs/' + encodeURIComponent(id)); } catch (e) {
      if (rid === state.rid) $('main').innerHTML = `<a class="back" href="#/my">← Back</a><div class="card empty">${esc(e.message)}</div>`;
      return;
    }
    if (rid !== state.rid) return;
    const { pr, runs, decisions = [], can_decide: canDecide = false } = data;
    const latest = runs[0];
    const gh = 'https://github.com/' + pr.repo + '/pull/' + pr.number;
    $('main').innerHTML = `
      <a class="back" id="back" href="#/my">← Back</a>
      <h1>${esc(pr.title) || '(no title)'} ${prStatus(pr)}</h1>
      <p class="muted"><span class="mono">${esc(pr.repo)}#${pr.number}</span> · by ${esc(pr.author)} · into ${esc(pr.base_ref || 'main')} ·
        <a href="${esc(gh)}" target="_blank" rel="noopener noreferrer">View on GitHub</a></p>
      ${pr.reviewers.length ? `<p>Requested reviewers: <span class="chips">${pr.reviewers.map((r) => `<span class="chip">${esc(r)}</span>`).join('')}</span></p>` : ''}
      ${decideCard(pr, latest, canDecide)}
      <h2>AI review history</h2>
      <div class="card">${runs.length ? runs.map((r, i) => runHTML(r, i === 0, decisions.filter((d) => d.run_id === r.id))).join('') : '<div class="empty">No AI review has run for this PR.</div>'}</div>`;
    $('back').onclick = (e) => { if (history.length > 1) { e.preventDefault(); history.back(); } };
    if (canDecide) wireDecision(rid, pr, latest);
  }

  const DEFAULT_REJECT_REASON = 'PR description and code changes do not match.';

  // The AI could not confirm the PR matches its description: a human decides.
  function decideCard(pr, latest, canDecide) {
    if (!latest || latest.status !== 'needs_human' || pr.state !== 'open') return '';
    if (!canDecide) {
      return '<div class="banner"><strong>Waiting for human review.</strong> A requested reviewer or an admin needs to approve or reject this pull request.</div>';
    }
    return `<div class="card pad decide">
      <h2>Human review needed</h2>
      <p class="muted">The AI could not confirm this pull request matches its description. Approve to let it continue, or reject to close it and tell the author why.</p>
      <div class="toolbar"><button class="primary" id="approve">Approve</button><button id="reject">Reject…</button></div>
      <div id="rejectbox" hidden>
        <label for="reason">Reason (posted to the pull request)</label>
        <textarea id="reason" rows="3" maxlength="2000">${esc(DEFAULT_REJECT_REASON)}</textarea>
        <div class="toolbar"><button class="danger" id="confirmreject">Reject and close PR</button><button id="cancelreject">Cancel</button></div>
      </div>
      <div class="error" id="decerr"></div>
    </div>`;
  }

  function wireDecision(rid, pr, latest) {
    const send = async (decision, reason) => {
      $('decerr').textContent = '';
      document.querySelectorAll('.decide button').forEach((b) => (b.disabled = true));
      try {
        // run_id pins the decision to the exact run on screen: if the PR changed meanwhile, the server refuses.
        await api('/api/v1/prs/' + pr.id + '/decision', { method: 'POST', body: JSON.stringify({ run_id: latest.id, decision, reason }) });
        if (rid === state.rid) renderPR(rid, pr.id);
      } catch (e) {
        if (e.message === 'signed out') return;
        $('decerr').textContent = e.message;
        document.querySelectorAll('.decide button').forEach((b) => (b.disabled = false));
      }
    };
    $('approve').onclick = () => send('approve', '');
    $('reject').onclick = () => { $('rejectbox').hidden = false; $('reject').hidden = true; $('reason').focus(); $('reason').select(); };
    $('cancelreject').onclick = () => { $('rejectbox').hidden = true; $('reject').hidden = false; $('decerr').textContent = ''; };
    $('confirmreject').onclick = () => {
      const reason = $('reason').value.trim();
      if (!reason) { $('decerr').textContent = 'A reason is required to reject.'; return; }
      send('reject', reason);
    };
  }

  function runHTML(r, latest, decisions = []) {
    let rep = null;
    try { rep = r.report ? JSON.parse(r.report) : null; } catch {}
    const list = (arr) => arr && arr.length ? '<ul>' + arr.map((x) => `<li>${x}</li>`).join('') + '</ul>' : '';
    let body = '';
    if (r.error) body += `<div class="error">${esc(r.error)}</div>`;
    if (rep) {
      body += `<div>${esc(rep.summary)}</div>`;
      if (rep.unrelated_changes && rep.unrelated_changes.length) body += '<h3>Unrelated changes</h3>' + list(rep.unrelated_changes.map(esc));
      if (rep.issues && rep.issues.length) body += '<h3>Issues</h3>' + list(rep.issues.map((i) => `<strong>${esc(i.severity)}</strong> <span class="mono">${esc(i.file)}</span>: ${esc(i.description)}`));
      if (rep.env_var_changes && rep.env_var_changes.length) body += '<h3>Env var changes</h3>' + list(rep.env_var_changes.map((v) => `<span class="mono">${esc(v)}</span>`));
      if (rep.has_migrations) body += '<p><span class="badge b-amber">Database migrations detected</span></p>';
      if (rep.missing_tests) body += '<p class="muted">No tests accompany these changes.</p>';
    }
    for (const d of decisions) {
      body += `<div class="decision ${d.decision}"><strong>${d.decision === 'approved' ? 'Approved' : 'Rejected'} by @${esc(d.actor_login)}</strong>
        <span class="muted">${esc(when(d.created_at))}</span>${d.reason ? `<div>${esc(d.reason)}</div>` : ''}</div>`;
    }
    return `<div class="run ${latest ? '' : 'old'}"><div class="head">${badge(r.status)}<span class="mono">${short(r.sha)}</span>
      <span class="muted">${esc(when(r.created_at))}</span>${latest ? '<span class="badge b-blue">Latest</span>' : ''}</div>${body}</div>`;
  }

  // ---------- settings ----------
  function renderSettings() {
    stopRefresh();
    const u = state.user, cur = theme.get();
    shell('settings', `
      <h1>Settings</h1>
      ${u.role === 'pending' ? '<div class="banner"><strong>Awaiting approval.</strong> An admin needs to approve your account before you can see pull requests. You can already sign in and change your theme.</div>' : ''}
      <div class="card pad">
        <div class="profile">
          ${u.avatar_url ? `<img class="avatar lg" src="${esc(u.avatar_url)}" alt="">` : ''}
          <div><strong>${esc(u.name || u.github_login)}</strong><br><a href="https://github.com/${encodeURIComponent(u.github_login)}" target="_blank" rel="noopener noreferrer">@${esc(u.github_login)}</a></div>
        </div>
        <dl class="kv">
          <dt>GitHub username</dt><dd>${esc(u.github_login)}</dd>
          <dt>Name</dt><dd>${esc(u.name) || '<span class="muted">not set on GitHub</span>'}</dd>
          <dt>Email</dt><dd>${esc(u.email) || '<span class="muted">not shared by GitHub</span>'}</dd>
          <dt>Role</dt><dd>${esc(roleLabel(u.role))}</dd>
          <dt>Member since</dt><dd>${esc(when(u.created_at))}</dd>
          <dt>Last sign-in</dt><dd>${esc(when(u.last_login_at))}</dd>
        </dl>
        <p class="muted">These details come from GitHub and are refreshed each time you sign in. They can't be edited here.</p>
      </div>
      <div class="card pad">
        <h2>Appearance</h2>
        <div class="seg" role="group" aria-label="Theme">
          ${['system', 'light', 'dark'].map((t) => `<button data-theme-btn="${t}" aria-pressed="${cur === t}">${t[0].toUpperCase() + t.slice(1)}</button>`).join('')}
        </div>
      </div>`);
    document.querySelectorAll('[data-theme-btn]').forEach((b) => {
      b.onclick = () => {
        theme.set(b.dataset.themeBtn);
        document.querySelectorAll('[data-theme-btn]').forEach((x) => x.setAttribute('aria-pressed', String(x === b)));
      };
    });
  }

  // ---------- admin ----------
  const ADMIN_TABS = [['overview', 'Overview'], ['prs', 'All pull requests'], ['users', 'Users']];
  function adminShell(sub, inner) {
    shell('admin', `<h1>Admin</h1><div class="tabs">${ADMIN_TABS.map(([k, l]) => `<a href="#/admin${k === 'overview' ? '' : '/' + k}" class="${sub === k ? 'active' : ''}">${l}</a>`).join('')}</div>${inner}`);
  }

  async function renderAdmin(rid, sub) {
    stopRefresh();
    if (!isAdmin(state.user.role)) { location.hash = '#/my'; return; }
    if (sub === 'prs') return adminPRs(rid);
    if (sub === 'users') return adminUsers(rid);
    return adminOverview(rid);
  }

  async function adminOverview(rid) {
    adminShell('overview', '<div id="ov" class="muted">Loading…</div>');
    const load = async () => {
      try {
        const [s, sync] = await Promise.all([api('/api/v1/admin/stats'), api('/api/v1/admin/sync')]);
        if (rid !== state.rid) return;
        $('ov').className = '';
        $('ov').innerHTML = overviewHTML(s, sync);
        $('chartbox').appendChild(weeklyChart(s.weekly));
        const btn = $('syncbtn');
        if (btn) btn.onclick = async () => { btn.disabled = true; try { await api('/api/v1/admin/sync', { method: 'POST' }); setTimeout(load, 1500); } catch (e) { showError(e); btn.disabled = false; } };
      } catch (e) { if (rid === state.rid) showError(e); }
    };
    await load();
    startRefresh(load);
  }

  const AI_ORDER = ['ai_passed', 'needs_human', 'blocked', 'error', 'ai_review', 'queued', 'none'];
  function barTable(rows, total) {
    return rows.length ? `<table class="bars"><tbody>${rows.map(([name, n, extra]) => `
      <tr><td>${name}</td><td><progress max="${Math.max(total, 1)}" value="${n}" aria-label="${n}"></progress></td><td>${n}${extra ? ` <span class="muted">${esc(extra)}</span>` : ''}</td></tr>`).join('')}</tbody></table>`
      : '<div class="empty">No data yet.</div>';
  }

  function overviewHTML(s, sync) {
    const aiRows = AI_ORDER.filter((k) => s.by_ai_status[k]).map((k) => [badge(k), s.by_ai_status[k]]);
    const maxA = Math.max(1, ...(s.top_authors.map((a) => a.total)));
    const maxR = Math.max(1, ...(s.awaiting_review.map((a) => a.open)));
    const maxP = Math.max(1, ...(s.by_repo.map((a) => a.total)));
    let syncMsg = !sync.enabled ? 'Backfill needs the GitHub App.' : sync.running ? 'Syncing from GitHub…'
      : sync.finished_at ? `Last sync ${ago(sync.finished_at)}: ${sync.prs} pull requests${sync.error ? ' (some repos failed)' : ''}.` : 'Not synced since start.';
    return `
      <div class="tiles">
        ${[['Open', s.open], ['Merged', s.merged], ['Closed unmerged', s.closed], ['Open drafts', s.drafts], ['Avg time to merge', hours(s.avg_hours_to_merge)]]
          .map(([l, n]) => `<div class="tile"><div class="n">${esc(n)}</div><div class="l">${esc(l)}</div></div>`).join('')}
      </div>
      <div class="card pad"><h2>Pull requests per week<span class="sub">last 12 weeks</span></h2><div id="chartbox"></div></div>
      <div class="grid2">
        <div class="card pad"><h2>AI review status<span class="sub">open PRs</span></h2>${barTable(aiRows, s.open)}</div>
        <div class="card pad"><h2>Waiting on reviewer<span class="sub">open PRs</span></h2>${barTable(s.awaiting_review.map((a) => [esc(a.name), a.open]), maxR)}</div>
        <div class="card pad"><h2>Top authors<span class="sub">all tracked PRs</span></h2>${barTable(s.top_authors.map((a) => [esc(a.name), a.total, a.open ? a.open + ' open' : '']), maxA)}</div>
        <div class="card pad"><h2>By repository</h2>${barTable(s.by_repo.map((a) => [esc(a.name), a.total, a.open ? a.open + ' open' : '']), maxP)}</div>
      </div>
      <div class="card pad"><div class="toolbar"><span class="muted">${esc(syncMsg)} Covers the last ${sync.days || 90} days. New activity arrives live through webhooks.</span><span class="grow"></span>
        <button id="syncbtn" ${!sync.enabled || sync.running ? 'disabled' : ''}>Sync now</button></div></div>`;
  }

  // Grouped bars: opened vs merged per week. One axis, thin rounded bars, a legend,
  // a hover/focus tooltip, and a table view so no value depends on hover or color.
  const SVGNS = 'http://www.w3.org/2000/svg';
  const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const weekLabel = (w) => MONTHS[Number(w.slice(5, 7)) - 1] + ' ' + Number(w.slice(8, 10));
  function svg(tag, attrs, parent) {
    const el = document.createElementNS(SVGNS, tag);
    for (const k in attrs) el.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(el);
    return el;
  }

  function weeklyChart(weeks) {
    const wrap = document.createElement('div');
    wrap.className = 'chart';
    const W = 720, H = 220, L = 30, R = 6, T = 8, B = 24, pw = W - L - R, ph = H - T - B;
    const peak = Math.max(0, ...weeks.map((w) => Math.max(w.opened, w.merged)));
    const step = Math.max(1, Math.ceil(peak / 4)), max = step * 4;
    const y = (v) => T + ph - (v / max) * ph;

    const legend = document.createElement('div');
    legend.className = 'legend';
    legend.innerHTML = '<span><i class="s1"></i>Opened</span><span><i class="s2"></i>Merged</span>';
    wrap.appendChild(legend);

    const root = svg('svg', { viewBox: `0 0 ${W} ${H}`, role: 'group', 'aria-label': 'Pull requests opened and merged per week' }, wrap);
    for (let v = 0; v <= max; v += step) {
      svg('line', { class: 'gridline', x1: L, x2: W - R, y1: y(v), y2: y(v) }, root);
      svg('text', { class: 'tick', x: L - 6, y: y(v) + 4, 'text-anchor': 'end' }, root).textContent = v;
    }
    const slot = pw / weeks.length, bw = 11, gap = 2;
    const tip = document.createElement('div');
    tip.className = 'tip';
    tip.hidden = true;
    wrap.appendChild(tip);

    const bar = (x, v, cls, parent) => { // 4px rounded data-end, flat on the baseline
      if (!v) return;
      const h = y(0) - y(v), r = Math.min(4, h, bw / 2);
      svg('path', { class: 'bar ' + cls, d: `M${x},${y(0)} v${-(h - r)} a${r},${r} 0 0 1 ${r},${-r} h${bw - 2 * r} a${r},${r} 0 0 1 ${r},${r} v${h - r} z` }, parent);
    };
    const show = (rect, w) => {
      tip.replaceChildren();
      const head = document.createElement('div');
      head.className = 'muted';
      head.textContent = 'Week of ' + weekLabel(w.week);
      tip.appendChild(head);
      [['s1', w.opened, 'opened'], ['s2', w.merged, 'merged']].forEach(([c, n, name]) => {
        const row = document.createElement('div'), key = document.createElement('span'), val = document.createElement('strong'), lab = document.createElement('span');
        row.className = 'row'; key.className = 'key ' + c; val.textContent = n; lab.className = 'muted'; lab.textContent = name;
        row.append(key, val, lab);
        tip.appendChild(row);
      });
      const wr = wrap.getBoundingClientRect(), rr = rect.getBoundingClientRect();
      tip.hidden = false;
      tip.style.left = Math.min(Math.max(rr.left - wr.left + rr.width / 2, 60), wr.width - 60) + 'px'; // CSSOM: allowed under CSP
    };

    weeks.forEach((w, i) => {
      const g = svg('g', { class: 'week' }, root), x0 = L + i * slot, gx = x0 + (slot - (2 * bw + gap)) / 2;
      bar(gx, w.opened, 's1', g);
      bar(gx + bw + gap, w.merged, 's2', g);
      const hit = svg('rect', { class: 'hit', x: x0, y: T, width: slot, height: ph, tabindex: 0, role: 'img',
        'aria-label': `Week of ${weekLabel(w.week)}: ${w.opened} opened, ${w.merged} merged` }, g);
      hit.addEventListener('pointerenter', () => show(hit, w));
      hit.addEventListener('pointermove', () => show(hit, w));
      hit.addEventListener('focus', () => show(hit, w));
      hit.addEventListener('pointerleave', () => { tip.hidden = true; });
      hit.addEventListener('blur', () => { tip.hidden = true; });
      if (i % 2 === 0) svg('text', { class: 'tick', x: x0 + slot / 2, y: H - 6, 'text-anchor': 'middle' }, root).textContent = weekLabel(w.week);
    });

    const det = document.createElement('details');
    det.className = 'table-view';
    det.innerHTML = `<summary>Show as table</summary><table><thead><tr><th>Week of</th><th>Opened</th><th>Merged</th></tr></thead><tbody>${
      weeks.map((w) => `<tr><td>${esc(weekLabel(w.week))}</td><td>${w.opened}</td><td>${w.merged}</td></tr>`).join('')}</tbody></table>`;
    wrap.appendChild(det);
    return wrap;
  }

  async function adminPRs(rid) {
    adminShell('prs', `<div class="toolbar">
      <select id="f-state" aria-label="State"><option value="">All states</option><option value="open">Open</option><option value="merged">Merged</option><option value="closed">Closed</option></select>
      <input id="f-repo" placeholder="repo (org/name)" aria-label="Repository">
      <input id="f-author" placeholder="author" aria-label="Author">
      <input id="f-reviewer" placeholder="reviewer" aria-label="Reviewer">
      <span class="grow"></span><span class="muted" id="count"></span></div><div id="listbox"></div>`);
    const load = async () => {
      const q = new URLSearchParams();
      for (const [k, id] of [['state', 'f-state'], ['repo', 'f-repo'], ['author', 'f-author'], ['reviewer', 'f-reviewer']]) {
        const v = $(id).value.trim(); if (v) q.set(k, v);
      }
      q.set('limit', '200');
      try {
        const prs = await api('/api/v1/admin/prs?' + q);
        if (rid !== state.rid) return;
        $('listbox').innerHTML = prTable(prs, { author: true, reviewers: true });
        $('count').textContent = prs.length === 200 ? 'Showing the latest 200' : prs.length + ' pull request' + (prs.length === 1 ? '' : 's');
        wireRows();
      } catch (e) { if (rid === state.rid) showError(e); }
    };
    let t;
    $('f-state').onchange = load;
    ['f-repo', 'f-author', 'f-reviewer'].forEach((id) => { $(id).oninput = () => { clearTimeout(t); t = setTimeout(load, 300); }; });
    await load();
    startRefresh(load);
  }

  // Every button below comes from `perms`, which the server computes for the signed-in admin and
  // enforces again on each request: admins can approve and promote; only the super-admin can
  // demote, deactivate or delete; nobody can change the super-admin.
  async function adminUsers(rid) {
    adminShell('users', '<div class="error" id="err"></div><div id="listbox" class="muted">Loading…</div>');
    let users;
    try { users = await api('/api/v1/admin/users'); } catch (e) { return showError(e); }
    if (rid !== state.rid) return;
    const pending = users.filter((u) => u.role === 'pending' && u.active).length;
    const isSuper = state.user.role === 'superadmin';
    const row = (u) => {
      const p = u.perms || { roles: [], active: false, delete: false };
      const opts = [u.role, ...p.roles];
      const canEdit = p.roles.length > 0 || p.active;
      const actions = [];
      if (u.role === 'pending' && p.roles.includes('user')) actions.push('<button class="primary approve">Approve</button>');
      if (u.role === 'user' && p.roles.includes('admin')) actions.push('<button class="promote">Make admin</button>');
      if (canEdit) actions.push('<button class="save">Save</button>');
      if (p.delete) actions.push('<button class="del">Delete</button>');
      const none = u.id === state.user.id ? 'you' : u.role === 'superadmin' ? 'owner' : '–';
      return `<tr data-id="${u.id}" data-login="${esc(u.github_login)}">
        <td>${u.avatar_url ? `<img class="avatar" src="${esc(u.avatar_url)}" alt=""> ` : ''}<strong>${esc(u.github_login || '(not signed in yet)')}</strong>
          ${u.role === 'superadmin' ? '<span class="badge b-purple">Super-admin</span>' : ''}${u.active ? '' : ' <span class="badge b-red">Deactivated</span>'}
          <br><span class="muted">${esc(u.name)}${u.email ? ' · ' + esc(u.email) : ''}</span></td>
        <td><select name="role" aria-label="Role" ${p.roles.length ? '' : 'disabled'}>${opts.map((r) => `<option value="${r}" ${u.role === r ? 'selected' : ''}>${esc(roleLabel(r))}</option>`).join('')}</select></td>
        <td><input type="checkbox" name="active" aria-label="Active" ${u.active ? 'checked' : ''} ${p.active ? '' : 'disabled'}></td>
        <td class="muted hide-sm">${esc(ago(u.last_login_at)) || '–'}</td>
        <td>${actions.length ? actions.join(' ') : `<span class="muted">${none}</span>`}</td></tr>`;
    };
    $('listbox').className = '';
    $('listbox').innerHTML = `${pending ? `<div class="banner"><strong>${pending} account${pending === 1 ? '' : 's'} awaiting approval.</strong></div>` : ''}
      <p class="muted">${isSuper ? 'You are the super-admin: you can change any role, deactivate accounts and delete them.'
        : 'Admins can approve pending accounts and promote users to admin. Demoting, deactivating or deleting accounts is for the super-admin.'}</p>
      <div class="card"><table><thead><tr><th>Account</th><th>Role</th><th>Active</th><th class="hide-sm">Last sign-in</th><th></th></tr></thead><tbody>
      ${users.map(row).join('')}
      </tbody></table></div>`;
    $('listbox').querySelector('tbody').onclick = async (e) => {
      const btn = e.target.closest('button'); if (!btn) return;
      const tr = btn.closest('tr'), q = (n) => tr.querySelector('[name=' + n + ']');
      const url = '/api/v1/admin/users/' + tr.dataset.id;
      try {
        if (btn.classList.contains('del')) {
          if (!confirm('Delete the account for ' + tr.dataset.login + '?\n\nThey lose access immediately. Their pull requests stay. If they sign in with GitHub again they start over as pending (deactivate instead to keep them out).')) return;
          await api(url, { method: 'DELETE' });
          return adminUsers(rid);
        }
        if (btn.classList.contains('approve')) q('role').value = 'user';
        if (btn.classList.contains('promote')) q('role').value = 'admin';
        await api(url, { method: 'PATCH', body: JSON.stringify({ role: q('role').value, active: q('active').checked }) });
        $('err').textContent = '';
        if (!btn.classList.contains('save')) return adminUsers(rid); // re-sort and refresh the buttons
        btn.textContent = 'Saved'; setTimeout(() => (btn.textContent = 'Save'), 1200);
      } catch (err) { $('err').textContent = err.message; }
    };
  }

  // ---------- routing ----------
  async function route() {
    const rid = ++state.rid;
    stopRefresh();
    if (!state.user) {
      try { state.user = await api('/auth/me'); } catch { return; } // api() already showed the login page
      if (rid !== state.rid) return;
    }
    const [a = '', b = ''] = location.hash.replace(/^#\/?/, '').split('/');
    if (state.user.role === 'pending') return renderSettings();
    switch (a) {
      case '': case 'my': return listPage(rid, 'my', 'My pull requests', '/api/v1/me/prs', { reviewers: true }, true);
      case 'reviews': return listPage(rid, 'reviews', 'Review pull requests', '/api/v1/me/reviews', { author: true }, false);
      case 'settings': return renderSettings();
      case 'admin': return renderAdmin(rid, b);
      case 'pr': return renderPR(rid, b);
      default: location.hash = '#/my';
    }
  }

  window.addEventListener('hashchange', route);
  route();
})();
