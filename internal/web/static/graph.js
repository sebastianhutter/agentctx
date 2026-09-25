// The graph page: projects, tasks, repositories and log entries as one picture.
//
// No library and no build step, like the rest of this interface. A few hundred
// nodes is what one person's vault holds, which a plain O(n²) force layout
// settles in well under a second, so there is nothing a framework would buy.
(function () {
  "use strict";

  const SVGNS = "http://www.w3.org/2000/svg";
  const svg = document.getElementById("graph");
  const tip = document.getElementById("g-tip");
  const count = document.getElementById("g-count");
  const ui = {
    project: document.getElementById("g-project"),
    entries: document.getElementById("g-entries"),
    closed: document.getElementById("g-closed"),
    search: document.getElementById("g-search"),
  };

  // Rest lengths per relation: an entry sits close to what it was logged on,
  // a repository further out, where tasks from several projects can reach it.
  const LENGTH = { logged: 26, about: 90, in: 90, owns: 170, uses: 170, touches: 110, waits: 70, waited: 70 };
  const STRENGTH = { logged: 0.9, about: 0.03, in: 0.35, owns: 0.1, uses: 0.06, touches: 0.03, waits: 0.25, waited: 0.15 };

  let data = { nodes: [], edges: [] };
  let nodes = [], edges = [], byId = new Map(), neighbours = new Map();
  const positions = new Map(); // survives a reload, so toggling a filter does not reshuffle
  let alpha = 1, running = false;
  let view = { x: 0, y: 0, k: 1 };
  let focus = null; // a node id whose neighbourhood alone is shown
  let hover = null;
  let fitting = true; // follow the layout with the camera until the user takes over

  const viewport = el("g", { class: "viewport" });
  const edgeLayer = el("g", { class: "edges" });
  const nodeLayer = el("g", { class: "nodes" });
  viewport.append(edgeLayer, nodeLayer);
  const defs = el("defs");
  defs.innerHTML =
    '<marker id="arrow" viewBox="0 0 10 10" refX="10" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">' +
    '<path d="M 0 0 L 10 5 L 0 10 z" class="arrow"/></marker>';
  svg.append(defs, viewport);

  function el(name, attrs) {
    const e = document.createElementNS(SVGNS, name);
    for (const k in attrs || {}) e.setAttribute(k, attrs[k]);
    return e;
  }

  function radius(n) {
    switch (n.kind) {
      case "project": return 13 + Math.sqrt(n.weight) * 0.9;
      case "repo": return 8 + Math.sqrt(n.weight) * 1.3;
      case "task": return 5 + Math.sqrt(n.weight) * 1.1;
      default: return 3.5;
    }
  }

  // --- data -----------------------------------------------------------------

  async function load() {
    const p = ui.project.value;
    const url = new URL(location.href);
    if (p) url.searchParams.set("project", p); else url.searchParams.delete("project");
    if (ui.entries.checked) url.searchParams.set("entries", "1"); else url.searchParams.delete("entries");
    history.replaceState(null, "", url);
    focus = null;

    const res = await fetch("/graph.json" + (p ? "?project=" + encodeURIComponent(p) : ""));
    if (!res.ok) {
      count.textContent = "could not load the graph: " + (await res.text());
      return;
    }
    data = await res.json();
    data.nodes = data.nodes || [];
    data.edges = data.edges || [];
    fitting = true;
    rebuild();
  }

  // rebuild applies the filters and draws the DOM for what is left.
  function rebuild() {
    const showEntries = ui.entries.checked;
    const showClosed = ui.closed.checked;

    let keep = new Set();
    for (const n of data.nodes) {
      if (n.kind === "entry" && !showEntries) continue;
      if (n.kind === "task" && n.state === "closed" && !showClosed) continue;
      keep.add(n.id);
    }
    // An entry hangs off its task: hiding the task hides the entry.
    for (const e of data.edges) {
      if (e.rel === "logged" && !keep.has(e.target)) keep.delete(e.source);
    }
    if (focus && keep.has(focus)) {
      const near = new Set([focus]);
      for (const e of data.edges) {
        if (!keep.has(e.source) || !keep.has(e.target)) continue;
        if (e.source === focus) near.add(e.target);
        if (e.target === focus) near.add(e.source);
      }
      keep = near;
    }

    nodes = data.nodes.filter((n) => keep.has(n.id));
    edges = data.edges.filter((e) => keep.has(e.source) && keep.has(e.target));
    byId = new Map(nodes.map((n) => [n.id, n]));
    neighbours = new Map(nodes.map((n) => [n.id, new Set()]));
    for (const e of edges) {
      neighbours.get(e.source).add(e.target);
      neighbours.get(e.target).add(e.source);
    }

    layer();
    place();
    draw();
    const tasks = nodes.filter((n) => n.kind === "task").length;
    const repos = nodes.filter((n) => n.kind === "repo").length;
    const projects = nodes.filter((n) => n.kind === "project").length;
    const entries = nodes.filter((n) => n.kind === "entry").length;
    count.textContent =
      `${projects} projects · ${tasks} tasks · ${repos} repos` + (entries ? ` · ${entries} entries` : "") +
      (focus ? " · focused (double-click the background to clear)" : "");
    kick(1);
  }

  // layer assigns each node a column when one project is shown: blockers left
  // of what waits for them, the project before its first tasks, repositories
  // after the last. Pure forces tie a dependency chain into a knot; a column
  // per depth reads as the order the work has to happen in. With several
  // projects the columns would fight each other, so it is off there.
  const COLUMN = 130;
  function layer() {
    for (const n of nodes) n.col = null;
    if (!ui.project.value || focus) return;

    const blockers = new Map();
    for (const e of edges) {
      if (e.rel !== "waits" && e.rel !== "waited") continue;
      if (!blockers.has(e.source)) blockers.set(e.source, []);
      blockers.get(e.source).push(e.target);
    }
    const depth = new Map();
    const visit = (id, seen) => {
      if (depth.has(id)) return depth.get(id);
      if (seen.has(id)) return 0; // a cycle; the checks refuse these, but do not hang on one
      seen.add(id);
      let d = 0;
      for (const b of blockers.get(id) || []) d = Math.max(d, visit(b, seen) + 1);
      depth.set(id, d);
      return d;
    };
    let max = 0;
    for (const n of nodes) if (n.kind === "task") max = Math.max(max, visit(n.id, new Set()));
    const mid = max / 2;
    for (const n of nodes) {
      if (n.kind === "task") n.col = (depth.get(n.id) - mid) * COLUMN;
      else if (n.kind === "project") n.col = (-mid - 1.2) * COLUMN;
      else if (n.kind === "repo") n.col = (mid + 1.4) * COLUMN;
    }
  }

  // place gives every node a starting point: where it was before, else near
  // whatever it hangs off, else on a ring of projects.
  function place() {
    const projects = nodes.filter((n) => n.kind === "project");
    const ring = Math.max(80, projects.length * 35);
    projects.forEach((n, i) => {
      if (positions.has(n.id)) return;
      const a = (2 * Math.PI * i) / Math.max(1, projects.length);
      positions.set(n.id, { x: Math.cos(a) * ring, y: Math.sin(a) * ring, vx: 0, vy: 0 });
    });
    const order = ["task", "repo", "entry"];
    for (const kind of order) {
      for (const n of nodes) {
        if (n.kind !== kind || positions.has(n.id)) continue;
        let anchor = null;
        for (const m of neighbours.get(n.id)) if (positions.has(m)) { anchor = positions.get(m); break; }
        const a = Math.random() * 2 * Math.PI, d = kind === "entry" ? 20 : 60;
        positions.set(n.id, {
          x: (anchor ? anchor.x : 0) + Math.cos(a) * d,
          y: (anchor ? anchor.y : 0) + Math.sin(a) * d,
          vx: 0, vy: 0,
        });
      }
    }
    for (const n of nodes) {
      n.p = positions.get(n.id);
      n.r = radius(n);
      if (n.col != null && Math.abs(n.p.x - n.col) > COLUMN) n.p.x = n.col + (Math.random() - 0.5) * 20;
    }
  }

  // --- drawing --------------------------------------------------------------

  function draw() {
    edgeLayer.replaceChildren();
    nodeLayer.replaceChildren();

    for (const e of edges) {
      const line = el("line", { class: "edge rel-" + e.rel });
      if (e.rel === "waits" || e.rel === "waited") line.setAttribute("marker-end", "url(#arrow)");
      e.line = line;
      e.s = byId.get(e.source);
      e.t = byId.get(e.target);
      edgeLayer.append(line);
    }

    // Big things underneath, so an entry dot is never hidden by its project.
    const sorted = nodes.slice().sort((a, b) => b.r - a.r);
    for (const n of sorted) {
      const g = el("g", { class: `node kind-${n.kind} state-${n.state || "none"}` });
      let shape;
      if (n.kind === "repo") {
        const s = n.r * 1.7;
        shape = el("rect", { x: -s / 2, y: -s / 2, width: s, height: s, rx: 3 });
      } else {
        shape = el("circle", { r: n.r });
      }
      g.append(shape);
      if (n.kind !== "entry") {
        const label = el("text", { y: n.r + 12, class: "label" });
        label.textContent = n.kind === "project" ? n.label : shorten(n.label, 28);
        g.append(label);
      }
      g.addEventListener("pointerenter", () => setHover(n));
      g.addEventListener("pointerleave", () => setHover(null));
      g.addEventListener("pointerdown", (ev) => startDrag(ev, n));
      g.addEventListener("dblclick", (ev) => {
        ev.stopPropagation();
        focus = n.id;
        fitting = true;
        rebuild();
      });
      n.g = g;
      nodeLayer.append(g);
    }
    highlight();
  }

  function shorten(s, max) {
    return s.length > max ? "…" + s.slice(s.length - max + 1) : s;
  }

  function render() {
    for (const e of edges) {
      const s = e.s.p, t = e.t.p;
      let x2 = t.x, y2 = t.y;
      if (e.line.hasAttribute("marker-end")) {
        // Stop the arrow at the rim of the node it points at.
        const dx = t.x - s.x, dy = t.y - s.y, d = Math.hypot(dx, dy) || 1;
        x2 = t.x - (dx / d) * (e.t.r + 2);
        y2 = t.y - (dy / d) * (e.t.r + 2);
      }
      e.line.setAttribute("x1", s.x);
      e.line.setAttribute("y1", s.y);
      e.line.setAttribute("x2", x2);
      e.line.setAttribute("y2", y2);
    }
    for (const n of nodes) n.g.setAttribute("transform", `translate(${n.p.x},${n.p.y})`);
    viewport.setAttribute("transform", `translate(${view.x},${view.y}) scale(${view.k})`);
  }

  // highlight dims everything but the hovered node's neighbourhood, or
  // everything that does not match the search.
  function highlight() {
    const q = ui.search.value.trim().toLowerCase();
    let lit = null;
    if (hover) {
      lit = new Set([hover.id, ...neighbours.get(hover.id)]);
    } else if (q) {
      lit = new Set(
        nodes.filter((n) => (n.label + " " + (n.title || "") + " " + (n.detail || "")).toLowerCase().includes(q)).map((n) => n.id)
      );
    }
    for (const n of nodes) n.g.classList.toggle("dim", !!lit && !lit.has(n.id));
    for (const e of edges) {
      const on = lit && (hover ? e.source === hover.id || e.target === hover.id : lit.has(e.source) && lit.has(e.target));
      e.line.classList.toggle("dim", !!lit && !on);
      e.line.classList.toggle("lit", !!on);
    }
  }

  function setHover(n) {
    if (drag) return;
    hover = n;
    highlight();
    if (!n) { tip.hidden = true; return; }
    const kind = n.kind === "entry" ? `${n.label} · ${n.project}` : n.kind;
    let html = `<div class="tip-kind">${esc(kind)}${n.state ? " · " + esc(n.state) : ""}</div>`;
    html += `<div class="tip-title">${esc(n.kind === "entry" ? n.title : n.label)}</div>`;
    if (n.kind !== "entry" && n.title && n.title !== n.label) html += `<div>${esc(n.title)}</div>`;
    if (n.detail) html += `<div class="muted">${esc(n.kind === "repo" ? "projects: " + n.detail : n.detail)}</div>`;
    if (n.kind === "task" || n.kind === "project") html += `<div class="muted">${n.weight} entries${n.ts ? " · last " + esc(n.ts) : ""}</div>`;
    if (n.kind === "repo") html += `<div class="muted">${n.weight} repo-scoped findings</div>`;
    if (n.kind === "entry" && n.ts) html += `<div class="muted">${esc(n.ts)}</div>`;
    tip.innerHTML = html;
    tip.hidden = false;
  }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }

  svg.addEventListener("pointermove", (ev) => {
    if (tip.hidden) return;
    const box = svg.getBoundingClientRect();
    tip.style.left = Math.min(ev.clientX - box.left + 14, box.width - 320) + "px";
    tip.style.top = ev.clientY - box.top + 14 + "px";
  });

  // --- simulation -----------------------------------------------------------

  function kick(a) {
    alpha = Math.max(alpha, a);
    if (!running) { running = true; requestAnimationFrame(tick); }
  }

  function tick() {
    step();
    if (fitting) fit();
    render();
    alpha *= 0.975;
    if (alpha > 0.004 || drag) requestAnimationFrame(tick);
    else running = false;
  }

  function step() {
    const n = nodes.length;
    // Repulsion, scaled by size so a project pushes its neighbours further out.
    for (let i = 0; i < n; i++) {
      const a = nodes[i];
      for (let j = i + 1; j < n; j++) {
        const b = nodes[j];
        let dx = b.p.x - a.p.x, dy = b.p.y - a.p.y;
        let d2 = dx * dx + dy * dy;
        if (d2 === 0) { dx = Math.random() - 0.5; dy = Math.random() - 0.5; d2 = 0.25; }
        if (d2 > 640000) continue; // 800px: too far to matter
        const charge = (a.kind === "entry" && b.kind === "entry" ? 60 : 1100) * (1 + (a.r + b.r) / 20);
        const d = Math.sqrt(d2);
        // Never closer than the two rims for the force: at near-zero distance
        // 1/d² flings a node off the canvas, and the fit then zooms out to it.
        const f = (charge * alpha) / Math.max(d2, (a.r + b.r) * (a.r + b.r), 100);
        const fx = (dx / d) * f, fy = (dy / d) * f;
        a.p.vx -= fx; a.p.vy -= fy;
        b.p.vx += fx; b.p.vy += fy;
      }
    }
    // Springs. In columns, a task's place is its column, so its pull on the
    // project and the repos is only a hint for the vertical order.
    const columns = nodes.some((nd) => nd.col != null);
    for (const e of edges) {
      const s = e.s.p, t = e.t.p;
      const dx = t.x - s.x, dy = t.y - s.y, d = Math.hypot(dx, dy) || 1;
      let want = (LENGTH[e.rel] || 100) + e.s.r + e.t.r;
      // A spring shorter than the gap between its two columns would pull
      // both ends onto one horizontal line; let it rest across the gap.
      if (e.s.col != null && e.t.col != null) want = Math.max(want, Math.abs(e.s.col - e.t.col) * 1.15);
      let strength = STRENGTH[e.rel] || 0.1;
      if (columns && (e.rel === "in" || e.rel === "touches" || e.rel === "owns" || e.rel === "uses")) strength *= 0.05;
      const f = ((d - want) / d) * strength * alpha;
      s.vx += dx * f; s.vy += dy * f;
      t.vx -= dx * f; t.vy -= dy * f;
    }
    // A weak pull to the middle keeps unconnected projects on screen.
    for (const nd of nodes) {
      const p = nd.p;
      if (nd.kind === "project" && nd.col != null) {
        p.x = nd.col; p.y = 0; p.vx = p.vy = 0;
        continue;
      }
      if (nd.col != null) {
        p.vx += (nd.col - p.x) * 0.2 * alpha;
      } else {
        p.vx -= p.x * 0.008 * alpha;
      }
      p.vy -= p.y * (nd.col != null ? 0.003 : 0.008) * alpha;
      if (nd === dragNode) { p.vx = p.vy = 0; continue; }
      p.vx *= 0.6; p.vy *= 0.6;
      const v = Math.hypot(p.vx, p.vy);
      if (v > 40) { p.vx *= 40 / v; p.vy *= 40 / v; }
      p.x += p.vx; p.y += p.vy;
    }
  }

  // --- interaction ----------------------------------------------------------

  let drag = null, dragNode = null;

  function toGraph(ev) {
    const box = svg.getBoundingClientRect();
    return { x: (ev.clientX - box.left - view.x) / view.k, y: (ev.clientY - box.top - view.y) / view.k };
  }

  function startDrag(ev, n) {
    ev.stopPropagation();
    const at = toGraph(ev);
    drag = { kind: "node", node: n, dx: n.p.x - at.x, dy: n.p.y - at.y, moved: false, x0: ev.clientX, y0: ev.clientY };
    dragNode = n;
    tip.hidden = true;
  }

  svg.addEventListener("pointerdown", (ev) => {
    fitting = false;
    drag = { kind: "pan", x: ev.clientX - view.x, y: ev.clientY - view.y, moved: false, x0: ev.clientX, y0: ev.clientY };
    svg.classList.add("panning");
  });

  // Moves and releases are followed on the window, not the SVG, so a drag
  // survives leaving the canvas — and without pointer capture, which would
  // retarget the double-click that focuses a node.
  window.addEventListener("pointermove", (ev) => {
    if (!drag) return;
    if (Math.hypot(ev.clientX - drag.x0, ev.clientY - drag.y0) > 3) drag.moved = true;
    if (drag.kind === "pan") {
      view.x = ev.clientX - drag.x;
      view.y = ev.clientY - drag.y;
      render();
    } else if (drag.moved) {
      fitting = false;
      const at = toGraph(ev);
      drag.node.p.x = at.x + drag.dx;
      drag.node.p.y = at.y + drag.dy;
      kick(0.3);
    }
  });

  window.addEventListener("pointerup", (ev) => {
    const d = drag;
    drag = null;
    dragNode = null;
    svg.classList.remove("panning");
    if (d && d.kind === "node" && !d.moved && d.node.url) {
      // Wait a moment so a double-click can cancel the navigation.
      const n = d.node;
      clearTimeout(svg._open);
      svg._open = setTimeout(() => {
        if (ev.metaKey || ev.ctrlKey) window.open(n.url, "_blank");
        else location.href = n.url;
      }, 260);
    }
  });

  svg.addEventListener("dblclick", () => {
    clearTimeout(svg._open);
    if (focus) { focus = null; fitting = true; rebuild(); }
  });
  nodeLayer.addEventListener("dblclick", () => clearTimeout(svg._open), true);

  svg.addEventListener("wheel", (ev) => {
    ev.preventDefault();
    fitting = false;
    const box = svg.getBoundingClientRect();
    const mx = ev.clientX - box.left, my = ev.clientY - box.top;
    const k = Math.min(4, Math.max(0.15, view.k * Math.exp(-ev.deltaY * 0.0015)));
    view.x = mx - ((mx - view.x) * k) / view.k;
    view.y = my - ((my - view.y) * k) / view.k;
    view.k = k;
    render();
  }, { passive: false });

  // fit points the camera at everything drawn, leaving room for the legend.
  function fit() {
    if (!nodes.length) return;
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const n of nodes) {
      x0 = Math.min(x0, n.p.x - n.r); x1 = Math.max(x1, n.p.x + n.r);
      y0 = Math.min(y0, n.p.y - n.r); y1 = Math.max(y1, n.p.y + n.r + 14);
    }
    const box = svg.getBoundingClientRect(), pad = 40;
    const k = Math.min(1.3, Math.max(0.15, Math.min((box.width - 2 * pad) / (x1 - x0 || 1), (box.height - 2 * pad) / (y1 - y0 || 1))));
    view = { k, x: box.width / 2 - ((x0 + x1) / 2) * k, y: box.height / 2 - ((y0 + y1) / 2) * k };
  }

  function centre() {
    const box = svg.getBoundingClientRect();
    view = { x: box.width / 2, y: box.height / 2, k: 0.8 };
  }

  ui.project.addEventListener("change", () => { positions.clear(); centre(); load(); });
  ui.entries.addEventListener("change", () => {
    const url = new URL(location.href);
    if (ui.entries.checked) url.searchParams.set("entries", "1"); else url.searchParams.delete("entries");
    history.replaceState(null, "", url);
    fitting = true;
    rebuild();
  });
  ui.closed.addEventListener("change", () => { fitting = true; rebuild(); });
  ui.search.addEventListener("input", highlight);

  const params = new URLSearchParams(location.search);
  if (params.get("entries") === "1") ui.entries.checked = true;
  centre();
  load();
})();
