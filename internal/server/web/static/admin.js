// Elko Feedback dashboard: a thin client of the REST API (/v1/admin/*).
"use strict";
const $ = (s, el = document) => el.querySelector(s);
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
const state = { offset: 0, limit: 50, filter: {} };

async function api(path, opts = {}) {
  const r = await fetch(path, { credentials: "same-origin", headers: { "Content-Type": "application/json" }, ...opts });
  if (r.status === 401) { showLogin(); throw new Error("auth"); }
  const ct = r.headers.get("content-type") || "";
  const body = ct.includes("json") ? await r.json() : await r.text();
  if (!r.ok) throw new Error(body.error || r.statusText);
  return body;
}

// password show/hide eye toggles
document.addEventListener("click", e => {
  const b = e.target.closest(".eye"); if (!b) return;
  const i = b.parentElement.querySelector("input");
  i.type = i.type === "password" ? "text" : "password";
  b.textContent = i.type === "password" ? "👁" : "🙈";
  b.setAttribute("aria-label", i.type === "password" ? "Show password" : "Hide password");
});

function showLogin() { $("#app").classList.add("hidden"); $("#login").classList.remove("hidden"); }
$("#loginForm").addEventListener("submit", async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  try {
    await api("/v1/admin/login", { method: "POST", body: JSON.stringify({ username: f.get("username"), password: f.get("password") }) });
    e.target.reset(); boot();
  } catch (err) { if (err.message !== "auth") { const el = $("#loginErr"); el.textContent = err.message; el.classList.remove("hidden"); } }
});
$("#logout").onclick = async () => { await api("/v1/admin/logout", { method: "POST" }).catch(() => {}); showLogin(); };

document.querySelectorAll(".tab").forEach(t => t.onclick = () => {
  document.querySelectorAll(".tab").forEach(x => x.classList.toggle("active", x === t));
  document.querySelectorAll(".tabpane").forEach(p => p.classList.toggle("hidden", p.id !== "tab-" + t.dataset.tab));
  $("#detail").classList.add("hidden");
  ({ inbox: loadFeedback, channels: loadChannels, keys: loadKeys, settings: loadSettings })[t.dataset.tab]();
});

async function boot() {
  try { const me = await api("/v1/admin/me"); $("#who").textContent = me.principal; } catch { return; }
  $("#login").classList.add("hidden"); $("#app").classList.remove("hidden");
  loadTree(); loadFeedback();
}

// channel tree with dotted-path prefixes
async function loadTree() {
  const { channels } = await api("/v1/admin/stats");
  const nodes = {};
  for (const c of channels || []) {
    const parts = c.channel.split(".");
    for (let i = 1; i <= parts.length; i++) {
      const p = parts.slice(0, i).join(".");
      nodes[p] = nodes[p] || { total: 0, new: 0, depth: i - 1 };
      nodes[p].total += c.total; nodes[p].new += c.new;
    }
  }
  const keys = Object.keys(nodes).sort();
  $("#tree").innerHTML = `<a href="#" data-p="" class="${state.filter.prefix ? "" : "sel"}"><b>All channels</b></a>` + keys.map(k =>
    `<a href="#" data-p="${esc(k)}" style="padding-left:${6 + nodes[k].depth * 14}px" class="${state.filter.prefix === k ? "sel" : ""}"><span>${esc(k.split(".").pop())}</span>${nodes[k].new ? `<span class="n">${nodes[k].new}</span>` : `<span class="muted">${nodes[k].total}</span>`}</a>`).join("");
  $("#tree").querySelectorAll("a").forEach(a => a.onclick = e => {
    e.preventDefault(); $("#filters").prefix.value = a.dataset.p; state.filter.prefix = a.dataset.p; state.offset = 0; loadTree(); loadFeedback();
  });
}

function qs(extra = {}) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries({ ...state.filter, ...extra })) if (v !== "" && v != null) p.set(k, v);
  return p.toString();
}
$("#filters").addEventListener("submit", e => {
  e.preventDefault();
  state.filter = Object.fromEntries(new FormData(e.target)); state.offset = 0; loadTree(); loadFeedback();
});
$("#prev").onclick = () => { state.offset = Math.max(0, state.offset - state.limit); loadFeedback(); };
$("#next").onclick = () => { state.offset += state.limit; loadFeedback(); };

async function loadFeedback() {
  const d = await api("/v1/admin/feedback?" + qs({ limit: state.limit, offset: state.offset }));
  $("#count").textContent = `${d.total} item${d.total === 1 ? "" : "s"}` + (d.total > state.limit ? ` · showing ${state.offset + 1}–${Math.min(d.total, state.offset + state.limit)}` : "");
  $("#expCsv").href = "/v1/admin/export?format=csv&" + qs(); $("#expJson").href = "/v1/admin/export?format=json&" + qs();
  $("#rows").innerHTML = (d.items || []).map(f => `<tr data-id="${f.id}">
    <td class="muted">${esc(f.created_at.replace("T", " ").slice(0, 16))}</td><td>${esc(f.channel)}</td>
    <td>${f.rating ? "★".repeat(f.rating) : ""}</td><td class="msg">${esc(f.message)}</td>
    <td><span class="pill s-${esc(f.status)}">${esc(f.status)}</span></td><td>${(f.tags || []).map(t => `<span class="tag">${esc(t)}</span>`).join("")}</td></tr>`).join("")
    || `<tr><td colspan="6" class="muted">No feedback matches.</td></tr>`;
  $("#rows").querySelectorAll("tr[data-id]").forEach(tr => tr.onclick = () => showDetail(tr.dataset.id));
  $("#prev").disabled = state.offset === 0; $("#next").disabled = state.offset + state.limit >= d.total;
}

async function showDetail(id) {
  let f = await api("/v1/admin/feedback/" + id);
  if (f.status === "new") f = await api("/v1/admin/feedback/" + id, { method: "PATCH", body: JSON.stringify({ status: "read" }) });
  const custom = f.custom && Object.keys(f.custom).length ? `<dt>custom</dt><dd>${esc(JSON.stringify(f.custom))}</dd>` : "";
  $("#detail").innerHTML = `<div class="row"><h2>#${f.id}</h2><button class="ghost" id="dClose">✕</button></div>
    <pre>${esc(f.message)}</pre>
    <dl><dt>channel</dt><dd>${esc(f.channel)}</dd><dt>rating</dt><dd>${f.rating ? "★".repeat(f.rating) : "—"}</dd>
    <dt>category</dt><dd>${esc(f.category) || "—"}</dd><dt>email</dt><dd>${f.email ? `<a href="mailto:${esc(f.email)}">${esc(f.email)}</a>` : "—"}</dd>
    <dt>app</dt><dd>${esc(f.app_version) || "—"}</dd><dt>source</dt><dd>${esc(f.source)}</dd><dt>referrer</dt><dd>${esc(f.referrer) || "—"}</dd>
    <dt>agent</dt><dd>${esc(f.user_agent)}</dd><dt>ip hash</dt><dd>${esc(f.ip_hash)}</dd><dt>created</dt><dd>${esc(f.created_at)}</dd>${custom}</dl>
    <label>Status <select id="dStatus">${["new","read","triaged","archived"].map(s => `<option${s === f.status ? " selected" : ""}>${s}</option>`).join("")}</select></label>
    <label>Tags (comma separated) <input id="dTags" value="${esc((f.tags || []).join(", "))}"></label>
    <label>Notes <textarea id="dNotes" rows="5">${esc(f.notes)}</textarea></label>
    <p><button class="primary" id="dSave">Save</button> <button class="ghost" id="dDel">Delete</button> <span id="dMsg" class="muted"></span></p>`;
  $("#detail").classList.remove("hidden");
  $("#dClose").onclick = () => $("#detail").classList.add("hidden");
  $("#dSave").onclick = async () => {
    await api("/v1/admin/feedback/" + id, { method: "PATCH", body: JSON.stringify({ status: $("#dStatus").value,
      tags: $("#dTags").value.split(",").map(s => s.trim()).filter(Boolean), notes: $("#dNotes").value }) });
    $("#dMsg").textContent = "Saved"; loadFeedback(); loadTree();
  };
  $("#dDel").onclick = async () => { if (!confirm("Delete feedback #" + id + "?")) return; await api("/v1/admin/feedback/" + id, { method: "DELETE" }); $("#detail").classList.add("hidden"); loadFeedback(); loadTree(); };
  loadFeedback(); loadTree();
}

// channels CRUD
let chEditing = null;
async function loadChannels() {
  const { channels } = await api("/v1/admin/channels");
  $("#chRows").innerHTML = (channels || []).map(c => `<tr><td><span class="dot" style="background:${esc(c.accent_color)}"></span>${esc(c.id)}</td><td>${esc(c.title)}</td>
    <td>${c.enabled ? "yes" : "no"}</td><td>${c.rate_per_hour}</td><td class="muted">${esc(c.allowed_origins) || "—"}</td>
    <td><a href="/f/${esc(c.id)}" target="_blank">/f/${esc(c.id)}</a></td><td><button class="ghost" data-edit="${esc(c.id)}">Edit</button> <button class="ghost" data-del="${esc(c.id)}">Delete</button></td></tr>`).join("");
  $("#chRows").querySelectorAll("[data-edit]").forEach(b => b.onclick = () => editChannel(channels.find(c => c.id === b.dataset.edit)));
  $("#chRows").querySelectorAll("[data-del]").forEach(b => b.onclick = async () => { if (!confirm("Delete channel " + b.dataset.del + "? Its feedback is kept.")) return; await api("/v1/admin/channels/" + encodeURIComponent(b.dataset.del), { method: "DELETE" }); loadChannels(); loadTree(); });
}
function editChannel(c) {
  chEditing = c ? c.id : null;
  c = c || { enabled: true, accent_color: "#f5b400", fields: { rating: true, email: true, app_version: true, category: ["bug", "idea", "praise", "other"] } };
  const f = $("#chForm");
  for (const k of ["id","title","description","accent_color","logo_url","rate_per_hour","max_bytes","allowed_origins","turnstile_sitekey","turnstile_secret","thanks_message"]) f[k].value = c[k] ?? "";
  f.id.readOnly = !!chEditing; f.enabled.checked = !!c.enabled; f.fields.value = JSON.stringify(c.fields || {}, null, 2);
  $("#chFormTitle").textContent = chEditing ? "Edit " + chEditing : "New channel"; $("#chErr").classList.add("hidden"); f.classList.remove("hidden");
}
$("#newCh").onclick = () => editChannel(null);
$("#chCancel").onclick = () => $("#chForm").classList.add("hidden");
$("#chForm").addEventListener("submit", async e => {
  e.preventDefault(); const f = e.target;
  try {
    const body = { title: f.title.value, description: f.description.value, accent_color: f.accent_color.value, logo_url: f.logo_url.value,
      rate_per_hour: +f.rate_per_hour.value || 0, max_bytes: +f.max_bytes.value || 0, allowed_origins: f.allowed_origins.value,
      turnstile_sitekey: f.turnstile_sitekey.value, turnstile_secret: f.turnstile_secret.value, thanks_message: f.thanks_message.value,
      enabled: f.enabled.checked, fields: JSON.parse(f.fields.value || "{}") };
    await api("/v1/admin/channels/" + encodeURIComponent(f.id.value), { method: "PUT", body: JSON.stringify(body) });
    f.classList.add("hidden"); loadChannels(); loadTree();
  } catch (err) { const el = $("#chErr"); el.textContent = err.message; el.classList.remove("hidden"); }
});

// API keys
async function loadKeys() {
  const { keys } = await api("/v1/admin/keys");
  $("#keyRows").innerHTML = (keys || []).map(k => `<tr><td>${esc(k.name)}</td><td><code>${esc(k.prefix)}…</code></td><td>${esc(k.created_at)}</td><td>${esc(k.last_used) || "never"}</td><td><button class="ghost" data-id="${k.id}">Revoke</button></td></tr>`).join("") || `<tr><td colspan="5" class="muted">No keys yet.</td></tr>`;
  $("#keyRows").querySelectorAll("[data-id]").forEach(b => b.onclick = async () => { if (!confirm("Revoke key?")) return; await api("/v1/admin/keys/" + b.dataset.id, { method: "DELETE" }); loadKeys(); });
}
$("#keyForm").addEventListener("submit", async e => {
  e.preventDefault(); const k = await api("/v1/admin/keys", { method: "POST", body: JSON.stringify({ name: e.target.name.value }) });
  const n = $("#newKey"); n.innerHTML = `New key <b>${esc(k.name)}</b> — copy it now, it will not be shown again:<br><code>${esc(k.key)}</code>`; n.classList.remove("hidden"); e.target.reset(); loadKeys();
});

// settings (config lives in the DB)
async function loadSettings() {
  const s = await api("/v1/admin/settings");
  $("#setFields").innerHTML = Object.keys(s).sort().map(k => `<label>${esc(k)} <input name="${esc(k)}" value="${esc(s[k])}"></label>`).join("");
}
$("#setForm").addEventListener("submit", async e => {
  e.preventDefault(); await api("/v1/admin/settings", { method: "PUT", body: JSON.stringify(Object.fromEntries(new FormData(e.target))) });
  $("#setMsg").textContent = "Saved"; setTimeout(() => $("#setMsg").textContent = "", 2000);
});

boot();
