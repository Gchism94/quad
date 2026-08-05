// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
)

// handleStudentPage serves the lightweight, framework-free student page at /me.
// It renders whether or not the React dashboard is mounted, and is public: the
// page itself carries no data — its inline JS fetches /me/work, which enforces the
// session and own-data scoping. An unauthenticated visitor sees a sign-in link.
func (s *Server) handleStudentPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(studentPageHTML))
}

const studentPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>My work — Cairn</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: system-ui, sans-serif; max-width: 820px; margin: 2rem auto; padding: 0 1rem; line-height: 1.5; color: #111; background: #fff; }
  @media (prefers-color-scheme: dark) { body { color: #eee; background: #161616; } a { color: #6ab0ff; } .card, details { border-color: #444; } .muted { color: #aaa; } }
  h1 { font-size: 1.5rem; }
  .muted { color: #555; }
  .card { border: 1px solid #ccc; border-radius: 8px; padding: 1rem; margin: 1rem 0; }
  .row { display: flex; flex-wrap: wrap; gap: .5rem 1rem; align-items: baseline; }
  .title { font-weight: 600; font-size: 1.1rem; }
  .score { font-variant-numeric: tabular-nums; font-weight: 600; }
  .pill { display: inline-block; padding: .1em .5em; border-radius: 999px; font-size: .8rem; border: 1px solid currentColor; }
  .running { color: #b06a00; }
  .pass { color: #1a7f37; }
  .fail { color: #b3261e; }
  details { border: 1px solid #ccc; border-radius: 6px; padding: .25rem .5rem; margin-top: .5rem; }
  summary { cursor: pointer; font-weight: 600; }
  table { border-collapse: collapse; width: 100%; margin: .5rem 0; }
  th, td { text-align: left; padding: .25rem .5rem; border-bottom: 1px solid #ddd; font-size: .95rem; }
  ul.tests { list-style: none; padding: 0; }
  ul.tests li { padding: .25rem 0; border-bottom: 1px solid #eee; }
  code { background: rgba(127,127,127,.15); padding: .05em .3em; border-radius: 3px; }
</style>
</head>
<body>
<h1>My work</h1>
<p id="status" class="muted" aria-live="polite">Loading…</p>
<div id="list"></div>

<script>
"use strict";
const $ = (sel, el) => (el || document).querySelector(sel);

function fmtDeadline(iso) {
  if (!iso) return "no deadline";
  try { return "due " + new Date(iso).toLocaleString(); } catch (e) { return "due " + iso; }
}
function fmtScore(g) {
  if (!g) return "not graded yet";
  return g.score + " / " + g.max_score;
}
function gradingBadge(item) {
  if (item.grading_status === "running") return '<span class="pill running">grading…</span>';
  if (item.grading_status === "failed") return '<span class="pill fail">grading failed</span>';
  return "";
}

function renderTests(tests) {
  if (!tests || !tests.length) return '<p class="muted">No per-test results.</p>';
  let html = '<ul class="tests">';
  for (const t of tests) {
    const cls = t.passed ? "pass" : "fail";
    const mark = t.passed ? "✓" : "✗";
    html += '<li><span class="' + cls + '">' + mark + " " + escapeHtml(t.name) + "</span> " +
            '<span class="muted">(' + t.points + "/" + t.max_points + ")</span>";
    if (t.detail) html += '<br><code>' + escapeHtml(t.detail) + "</code>";
    html += "</li>";
  }
  return html + "</ul>";
}
function renderHistory(history) {
  if (!history || !history.length) return "";
  let html = "<table><caption class='muted'>Attempt history</caption><thead><tr><th>When</th><th>Score</th></tr></thead><tbody>";
  for (const h of history) {
    let when = h.graded_at; try { when = new Date(h.graded_at).toLocaleString(); } catch (e) {}
    html += "<tr><td>" + escapeHtml(when) + "</td><td class='score'>" + h.score + " / " + h.max_score + "</td></tr>";
  }
  return html + "</tbody></table>";
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, c => ({ "&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;" }[c]));
}

async function loadDetail(id, container) {
  container.textContent = "Loading details…";
  try {
    const res = await fetch("/me/work/" + encodeURIComponent(id), { credentials: "same-origin" });
    if (!res.ok) { container.textContent = "Could not load details."; return; }
    const d = await res.json();
    container.innerHTML = renderTests(d.tests) + renderHistory(d.history);
  } catch (e) { container.textContent = "Could not load details."; }
}

function card(item) {
  const el = document.createElement("section");
  el.className = "card";
  const repo = item.repo_web_url
    ? '<a href="' + encodeURI(item.repo_web_url) + '" target="_blank" rel="noopener">repo ↗</a>'
    : '<span class="muted">repo not ready</span>';
  el.innerHTML =
    '<div class="row"><span class="title">' + escapeHtml(item.assignment_title || item.assignment_slug || "Assignment") + "</span> " +
    gradingBadge(item) + "</div>" +
    '<div class="row muted">' + escapeHtml(item.classroom_name || "") + " · " + repo + " · " +
    escapeHtml(fmtDeadline(item.deadline)) + " · status: " + escapeHtml(item.status || "") + "</div>" +
    '<div class="row">Score: <span class="score">' + escapeHtml(fmtScore(item.latest_grade)) + "</span></div>";

  const det = document.createElement("details");
  const sum = document.createElement("summary");
  sum.textContent = "Per-test results & history";
  const body = document.createElement("div");
  det.appendChild(sum); det.appendChild(body);
  let loaded = false;
  det.addEventListener("toggle", () => { if (det.open && !loaded) { loaded = true; loadDetail(item.submission_id, body); } });
  el.appendChild(det);
  return el;
}

let pollTimer = null;
async function load() {
  let res;
  try { res = await fetch("/me/work", { credentials: "same-origin" }); }
  catch (e) { $("#status").textContent = "Network error."; return; }

  if (res.status === 401) {
    $("#status").innerHTML = 'Please <a href="/student/login">sign in</a> to see your work.';
    $("#list").innerHTML = "";
    return;
  }
  if (!res.ok) { $("#status").textContent = "Could not load your work."; return; }

  const data = await res.json();
  const work = (data && data.work) || [];
  const list = $("#list");
  list.innerHTML = "";
  if (!work.length) {
    $("#status").textContent = "No submissions yet. Accept an assignment to get started.";
    return;
  }
  $("#status").textContent = work.length + (work.length === 1 ? " submission" : " submissions");
  for (const item of work) list.appendChild(card(item));

  // If anything is grading, poll until it settles, then stop.
  const anyRunning = work.some(i => i.grading_status === "running");
  if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
  if (anyRunning) pollTimer = setTimeout(load, 4000);
}

load();
</script>
</body>
</html>`
