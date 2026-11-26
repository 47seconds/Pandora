(() => {
  'use strict';
  const app = document.getElementById('app');
  const store = {
    get: () => { try { return localStorage.getItem('pandora_token') || ''; } catch { return ''; } },
    set: (t) => { try { t ? localStorage.setItem('pandora_token', t) : localStorage.removeItem('pandora_token'); } catch {} },
  };
  const state = { user: null, filter: 'open', timer: null };

  // PR titles, descriptions and AI text are untrusted: always escape before inserting.
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

  const STATUS = {
    queued: ['Queued', 'blue'], ai_review: ['Reviewing', 'blue'], ai_passed: ['AI passed', 'green'],
    needs_human: ['Needs human', 'amber'], blocked: ['Blocked', 'red'], error: ['Error', 'red'], superseded: ['Superseded', 'grey'],
  };
  const badge = (status) => { const [l, c] = STATUS[status] || [status || 'No run', 'grey']; return `<span class="badge b-${c}">${esc(l)}</span>`; };
  const short = (sha) => esc((sha || '').slice(0, 7));
  const when = (iso) => iso ? new Date(iso).toLocaleString() : '';
  const ago = (iso) => {
    const s = Math.max(0, (Date.now() - new Date(iso)) / 1000);
    if (s < 60) return 'just now';
    if (s < 3600) return Math.floor(s / 60) + 'm ago';
    if (s < 86400) return Math.floor(s / 3600) + 'h ago';
    return Math.floor(s / 86400) + 'd ago';
  };

  async function api(path, opts = {}) {
    const res = await fetch(path, {
      ...opts,
      headers: { 'Content-Type': 'application/json', ...(store.get() ? { Authorization: 'Bearer ' + store.get() } : {}) },
    });
    let body = null;
    try { body = await res.json(); } catch {}
    if (res.status === 401 && store.get()) { logout(); throw new Error('Session expired'); }
    if (!res.ok) throw new Error((body && body.error) || 'Request failed (' + res.status + ')');
    return body;
  }

  function logout() { store.set(''); state.user = null; stopRefresh(); render(); }
  function stopRefresh() { if (state.timer) { clearInterval(state.timer); state.timer = null; } }
  function startRefresh(fn) { stopRefresh(); state.timer = setInterval(() => { if (!document.hidden) fn(); }, 15000); }

  // ---------- views ----------
  function shell(active, inner) {
    const admin = state.user.role === 'admin';
    app.innerHTML = `
      <header>
        <a class="brand" href="#/">Pandora</a>
        <nav>
          <a href="#/" class="${active === 'prs' ? 'active' : ''}">Pull requests</a>
          ${admin ? `<a href="#/users" class="${active === 'users' ? 'active' : ''}">Users</a>` : ''}
        </nav>
        <span class="who">${esc(state.user.email)} · ${esc(state.user.role)}</span>
        <button id="logout">Log out</button>
      </header>
      <main>${inner}</main>`;
    document.getElementById('logout').onclick = logout;
  }

  function renderAuth(mode = 'login') {
    const signup = mode === 'signup';
    app.innerHTML = `
      <div class="login card pad">
        <h1>Pandora ${signup ? '· Sign up' : '· Log in'}</h1>
        <form id="f">
          ${signup ? '<label>Name<input name="name" autocomplete="name"></label>' : ''}
          <label>Email<input name="email" type="email" required autocomplete="email"></label>
          <label>Password<input name="password" type="password" required minlength="8" maxlength="72" autocomplete="${signup ? 'new-password' : 'current-password'}"></label>
          <div class="error" id="err"></div>
          <button class="primary" type="submit">${signup ? 'Create account' : 'Log in'}</button>
        </form>
        <p class="muted"><a href="#" id="toggle">${signup ? 'Have an account? Log in' : 'No account? Sign up'}</a></p>
      </div>`;
    document.getElementById('toggle').onclick = (e) => { e.preventDefault(); renderAuth(signup ? 'login' : 'signup'); };
    document.getElementById('f').onsubmit = async (e) => {
      e.preventDefault();
      const d = Object.fromEntries(new FormData(e.target));
      try {
        const r = await api(signup ? '/auth/signup' : '/auth/login', { method: 'POST', body: JSON.stringify(d) });
        store.set(r.token); state.user = r.user; render();
      } catch (err) { document.getElementById('err').textContent = err.message; }
    };
  }

  async function renderList() {
    const f = state.filter;
    if (!document.getElementById('tbody')) {
      shell('prs', `
        <h1>Pull requests</h1>
        <div class="tiles" id="tiles"></div>
        <div class="toolbar">
          <select id="filter">
            <option value="open">Open</option><option value="closed">Closed</option><option value="">All</option>
          </select>
          <span class="grow"></span><span class="muted" id="updated"></span>
        </div>
        <div class="card"><table><thead><tr><th>PR</th><th>Author</th><th>Status</th><th>Commit</th><th>Updated</th></tr></thead><tbody id="tbody"></tbody></table></div>`);
      const sel = document.getElementById('filter');
      sel.value = f;
      sel.onchange = () => { state.filter = sel.value; renderList(); };
    }
    let prs;
    try { prs = await api('/api/v1/prs?limit=200' + (f ? '&state=' + f : '')); } catch (e) { return fail(e); }
    if (!document.getElementById('tbody')) return; // user navigated away while loading
    const count = (s) => prs.filter((p) => p.latest_run && p.latest_run.status === s).length;
    const tiles = [['PRs shown', prs.length], ['AI passed', count('ai_passed')], ['Needs human', count('needs_human')], ['Blocked', count('blocked')], ['Errors', count('error')]];
    document.getElementById('tiles').innerHTML = tiles.map(([l, n]) => `<div class="tile"><div class="n">${n}</div><div class="l">${esc(l)}</div></div>`).join('');
    document.getElementById('updated').textContent = 'Updated ' + new Date().toLocaleTimeString();
    document.getElementById('tbody').innerHTML = prs.length ? prs.map((p) => {
      const r = p.latest_run;
      return `<tr class="click" data-id="${p.id}">
        <td><strong>${esc(p.title) || '(no title)'}</strong><br><span class="muted mono">${esc(p.repo)}#${p.number}</span></td>
        <td>${esc(p.author)}</td><td>${badge(r && r.status)}</td>
        <td class="mono">${r ? short(r.sha) : ''}</td><td class="muted" title="${esc(when(p.updated_at))}">${esc(ago(p.updated_at))}</td></tr>`;
    }).join('') : '<tr><td colspan="5" class="empty">No pull requests yet.</td></tr>';
    document.getElementById('tbody').onclick = (e) => { const tr = e.target.closest('tr[data-id]'); if (tr) location.hash = '#/pr/' + tr.dataset.id; };
    startRefresh(renderList);
  }

  async function renderPR(id) {
    stopRefresh();
    let prs, runs;
    try { [prs, runs] = await Promise.all([api('/api/v1/prs?limit=200'), api('/api/v1/prs/' + id + '/runs')]); } catch (e) { return fail(e); }
    const pr = prs.find((p) => String(p.id) === String(id));
    if (!pr) return shell('prs', '<a class="back" href="#/">← Back</a><div class="empty card">PR not found.</div>');
    const ghURL = 'https://github.com/' + pr.repo + '/pull/' + pr.number;
    shell('prs', `
      <a class="back" href="#/">← All pull requests</a>
      <h1>${esc(pr.title) || '(no title)'}</h1>
      <p class="muted"><span class="mono">${esc(pr.repo)}#${pr.number}</span> · by ${esc(pr.author)} · ${esc(pr.state)} ·
        <a href="${esc(ghURL)}" target="_blank" rel="noopener noreferrer">View on GitHub</a></p>
      <h2>Review history</h2>
      <div class="card">${runs.length ? runs.map((r, i) => runHTML(r, i === 0)).join('') : '<div class="empty">No runs yet.</div>'}</div>`);
  }

  function runHTML(r, latest) {
    let rep = null;
    try { rep = r.report ? JSON.parse(r.report) : null; } catch {}
    const list = (arr) => arr && arr.length ? '<ul>' + arr.map((x) => `<li>${x}</li>`).join('') + '</ul>' : '';
    let body = '';
    if (r.error) body += `<div class="error">${esc(r.error)}</div>`;
    if (rep) {
      body += `<div>${esc(rep.summary)}</div>`;
      if (rep.unrelated_changes && rep.unrelated_changes.length) body += '<h2>Unrelated changes</h2>' + list(rep.unrelated_changes.map(esc));
      if (rep.issues && rep.issues.length) body += '<h2>Issues</h2>' + list(rep.issues.map((i) => `<strong>${esc(i.severity)}</strong> <span class="mono">${esc(i.file)}</span>: ${esc(i.description)}`));
      if (rep.env_var_changes && rep.env_var_changes.length) body += '<h2>Env var changes</h2>' + list(rep.env_var_changes.map((v) => `<span class="mono">${esc(v)}</span>`));
      if (rep.has_migrations) body += '<p><span class="badge b-amber">Database migrations detected</span></p>';
      if (rep.missing_tests) body += '<p class="muted">No tests accompany these changes.</p>';
    }
    return `<div class="run ${latest ? '' : 'old'}">
      <div class="head">${badge(r.status)}<span class="mono">${short(r.sha)}</span>
        <span class="muted">${esc(when(r.created_at))}</span>${latest ? '<span class="badge b-blue">Latest</span>' : ''}</div>${body}</div>`;
  }

  async function renderUsers() {
    stopRefresh();
    if (state.user.role !== 'admin') { location.hash = '#/'; return; }
    let users;
    try { users = await api('/api/v1/users'); } catch (e) { return fail(e); }
    shell('users', `
      <h1>Users</h1>
      <div class="error" id="err"></div>
      <div class="card"><table><thead><tr><th>Email</th><th>Role</th><th>GitHub login</th><th>Active</th><th></th></tr></thead><tbody>
      ${users.map((u) => { const me = u.id === state.user.id; return `<tr data-id="${u.id}">
        <td>${esc(u.email)}<br><span class="muted">${esc(u.name)}</span></td>
        <td><select name="role" ${me ? 'disabled' : ''}>${['admin', 'reviewer', 'developer'].map((r) => `<option ${u.role === r ? 'selected' : ''}>${r}</option>`).join('')}</select></td>
        <td><input name="github_login" value="${esc(u.github_login)}" placeholder="not linked"></td>
        <td><input type="checkbox" name="active" ${u.active ? 'checked' : ''} ${me ? 'disabled' : ''}></td>
        <td><button class="save">Save</button></td></tr>`; }).join('')}
      </tbody></table></div>`);
    document.querySelector('tbody').onclick = async (e) => {
      if (!e.target.classList.contains('save')) return;
      const tr = e.target.closest('tr'), q = (n) => tr.querySelector('[name=' + n + ']');
      const body = { github_login: q('github_login').value };
      if (!q('role').disabled) { body.role = q('role').value; body.active = q('active').checked; }
      try {
        await api('/api/v1/users/' + tr.dataset.id, { method: 'PATCH', body: JSON.stringify(body) });
        document.getElementById('err').textContent = ''; e.target.textContent = 'Saved'; setTimeout(() => (e.target.textContent = 'Save'), 1200);
      } catch (err) { document.getElementById('err').textContent = err.message; }
    };
  }

  function fail(e) {
    if (!state.user) return;
    const el = document.querySelector('main');
    if (el) el.insertAdjacentHTML('afterbegin', `<div class="error">${esc(e.message)}</div>`);
  }

  async function render() {
    if (!store.get()) return renderAuth();
    if (!state.user) {
      try { state.user = await api('/auth/me'); } catch { return renderAuth(); }
    }
    const h = location.hash || '#/';
    if (h.startsWith('#/pr/')) return renderPR(h.slice(5));
    if (h === '#/users') return renderUsers();
    return renderList();
  }

  window.addEventListener('hashchange', () => { document.getElementById('tbody') && (app.innerHTML = ''); render(); });
  render();
})();
