// The graph page: projects, tasks, the repositories, environments and services
// they work on, and log entries as one picture, with a side panel that shows
// everything known about whatever is clicked.
//
// Drawn with Cytoscape.js (vendored under static/vendor, see LICENSES.txt).
// Two layouts: projects as clusters (fcose, which understands compound nodes)
// for the whole vault, and a left-to-right dependency flow (dagre) for one
// project, where the order the work has to happen in is the thing to see.
(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const ui = {
    project: $("g-project"), layout: $("g-layout"), entries: $("g-entries"),
    closed: $("g-closed"), search: $("g-search"), fit: $("g-fit"), count: $("g-count"),
    tip: $("g-tip"), panel: $("g-panel"), body: $("g-panel-body"), close: $("g-close"),
  };

  // Colours come from the stylesheet, so the graph follows light and dark mode
  // with the rest of the page.
  const css = getComputedStyle(document.documentElement);
  const c = (name) => css.getPropertyValue(name).trim();
  const colour = {
    project: c("--g-project"), repo: c("--g-repo"), environment: c("--g-environment"), service: c("--g-service"),
    ready: c("--g-ready"), blocked: c("--g-blocked"), closed: c("--g-closed"),
    eRepo: c("--g-e-repo"), eEnvironment: c("--g-e-environment"), eService: c("--g-e-service"),
    eProject: c("--g-e-project"), eTask: c("--g-e-task"),
    edge: c("--g-edge"), ink: c("--ink"), muted: c("--muted"), bg: c("--bg"), warn: c("--warn"),
  };

  // The node kinds findings accumulate about. Each is also the name of its
  // query parameter on /log and /graph/node, and the prefix of its node id.
  const SUBJECTS = new Set(["repo", "environment", "service"]);

  let data = { nodes: [], edges: [] };
  let focus = null; // node id whose neighbourhood alone is shown
  let selected = null; // node id whose details are in the panel
  const positions = new Map(); // kept across filter changes, so the picture does not reshuffle

  const cy = cytoscape({
    container: $("graph"),
    minZoom: 0.08,
    maxZoom: 3,
    wheelSensitivity: 0.25,
    boxSelectionEnabled: false,
    style: [
      { selector: "node", style: {
        label: "data(label)", "font-size": 10, color: colour.muted,
        "text-valign": "bottom", "text-margin-y": 3,
        "text-outline-color": colour.bg, "text-outline-width": 2,
        "border-width": 1.5, "border-color": colour.bg,
        "min-zoomed-font-size": 7,
      } },
      // A project with children is a cluster: a box around its tasks.
      { selector: "node[kind='project']:parent", style: {
        shape: "round-rectangle", "background-color": colour.project, "background-opacity": 0.05,
        "border-color": colour.project, "border-width": 2, "border-opacity": 0.6,
        "text-valign": "top", "text-halign": "center", "text-margin-y": -4,
        "font-size": 14, "font-weight": 650, color: colour.ink, padding: 18,
      } },
      { selector: "node[kind='project']:childless", style: {
        shape: "ellipse", "background-color": colour.project,
        width: "mapData(weight, 0, 150, 34, 70)", height: "mapData(weight, 0, 150, 34, 70)",
        "font-size": 13, "font-weight": 650, color: colour.ink,
      } },
      { selector: "node[kind='project'][state='unconsolidated']", style: { "border-style": "dashed" } },
      { selector: "node[kind='task']", style: {
        shape: "ellipse", width: "mapData(weight, 0, 40, 16, 42)", height: "mapData(weight, 0, 40, 16, 42)",
      } },
      { selector: "node[kind='task'][state='ready']", style: { "background-color": colour.ready } },
      { selector: "node[kind='task'][state='blocked']", style: { "background-color": colour.blocked } },
      { selector: "node[kind='task'][state='closed']", style: { "background-color": colour.closed } },
      { selector: "node[kind='repo']", style: {
        shape: "round-rectangle", "background-color": colour.repo, color: colour.ink,
        width: "mapData(weight, 0, 30, 20, 48)", height: "mapData(weight, 0, 30, 20, 48)",
      } },
      { selector: "node[kind='environment']", style: {
        shape: "hexagon", "background-color": colour.environment, color: colour.ink,
        width: "mapData(weight, 0, 30, 22, 50)", height: "mapData(weight, 0, 30, 20, 46)",
      } },
      { selector: "node[kind='service']", style: {
        shape: "round-diamond", "background-color": colour.service, color: colour.ink,
        width: "mapData(weight, 0, 30, 22, 50)", height: "mapData(weight, 0, 30, 22, 50)",
      } },
      { selector: "node[kind='repo'][state^='shared'], node[kind='environment'][state^='shared'], node[kind='service'][state^='shared']",
        style: { "border-color": colour.ink, "border-width": 3 } },
      { selector: "node[kind='repo'][state$='unconsolidated'], node[kind='environment'][state$='unconsolidated'], node[kind='service'][state$='unconsolidated']",
        style: { "background-opacity": 0.5 } },
      { selector: "node[kind='entry']", style: {
        width: 9, height: 9, label: "", "border-width": 0.8, "background-color": colour.eTask,
      } },
      { selector: "node[kind='entry'][state='repo']", style: { "background-color": colour.eRepo } },
      { selector: "node[kind='entry'][state='environment']", style: { "background-color": colour.eEnvironment } },
      { selector: "node[kind='entry'][state='service']", style: { "background-color": colour.eService } },
      { selector: "node[kind='entry'][state='project']", style: { "background-color": colour.eProject } },
      { selector: "node[kind='entry'][label='decision']", style: { shape: "diamond", width: 11, height: 11 } },
      { selector: "node[kind='entry'][label='note']", style: { shape: "round-rectangle" } },
      // Warnings from the checks page, drawn as a halo so they show at any zoom.
      { selector: "node[warn > 0]", style: {
        "underlay-color": colour.warn, "underlay-opacity": 0.35, "underlay-padding": 6, "underlay-shape": "ellipse",
      } },
      { selector: "edge", style: {
        width: 1, "line-color": colour.edge, "curve-style": "bezier", opacity: 0.8,
      } },
      { selector: "edge[rel='touches']", style: { width: "mapData(weight, 1, 12, 1, 5)" } },
      { selector: "edge[rel='logged']", style: { width: 0.6 } },
      { selector: "edge[rel='about']", style: { width: 0.6, "line-style": "dotted" } },
      { selector: "edge[rel='owns']", style: { width: 1.8 } },
      { selector: "edge[rel='uses']", style: { "line-color": colour.warn, "line-style": "dashed", width: 1.6 } },
      // A waits edge points at the task being waited for. In the flow layout
      // the element runs blocker → waiter so dagre ranks blockers first, and
      // the arrow is drawn at the source end instead.
      { selector: "edge[rel='waits'], edge[rel='waited']", style: {
        "line-color": colour.blocked, width: 2, "target-arrow-shape": "triangle",
        "target-arrow-color": colour.blocked, "arrow-scale": 1.1,
      } },
      { selector: "edge[rel='waited']", style: {
        "line-color": colour.closed, "target-arrow-color": colour.closed, "line-style": "dashed",
      } },
      { selector: "edge.flipped[rel='waits'], edge.flipped[rel='waited']", style: {
        "target-arrow-shape": "none", "source-arrow-shape": "triangle", "source-arrow-color": colour.blocked,
      } },
      { selector: "edge.flipped[rel='waited']", style: { "source-arrow-color": colour.closed } },
      { selector: ".faded", style: { opacity: 0.1, "text-opacity": 0 } },
      { selector: "node.hl", style: { "text-opacity": 1, color: colour.ink } },
      { selector: "edge.hl", style: { opacity: 1, width: 2.5 } },
      { selector: "node:selected", style: { "border-color": colour.ink, "border-width": 3 } },
    ],
  });

  // --- data and elements ----------------------------------------------------

  function layoutMode() {
    if (ui.layout.value !== "auto") return ui.layout.value;
    return ui.project.value ? "flow" : "clusters";
  }

  async function load() {
    syncURL();
    const p = ui.project.value;
    const res = await fetch("/graph.json" + (p ? "?project=" + encodeURIComponent(p) : ""));
    if (!res.ok) {
      ui.count.textContent = "could not load the graph: " + (await res.text());
      return;
    }
    data = await res.json();
    data.nodes = data.nodes || [];
    data.edges = data.edges || [];
    positions.clear();
    rebuild(true);
  }

  // rebuild turns graph.json into Cytoscape elements under the current
  // filters and layout mode, then lays them out.
  function rebuild(fresh) {
    const clusters = layoutMode() === "clusters";
    const showEntries = ui.entries.checked;
    const showClosed = ui.closed.checked;

    cy.nodes().forEach((n) => positions.set(n.id(), { ...n.position() }));

    let keep = new Set();
    for (const n of data.nodes) {
      if (n.kind === "entry" && !showEntries) continue;
      if (n.kind === "task" && n.state === "closed" && !showClosed) continue;
      keep.add(n.id);
    }
    // An entry hangs off what it was logged on: hiding that hides the entry.
    for (const e of data.edges) if (e.rel === "logged" && !keep.has(e.target)) keep.delete(e.source);

    if (focus && keep.has(focus)) {
      const near = new Set([focus]);
      for (const e of data.edges) {
        if (!keep.has(e.source) || !keep.has(e.target)) continue;
        if (e.source === focus) near.add(e.target);
        if (e.target === focus) near.add(e.source);
      }
      // Keep the boxes the focused nodes live in, so they stay readable.
      if (clusters) for (const n of data.nodes) if (near.has(n.id) && n.project) near.add("project:" + n.project);
      keep = near;
    } else {
      focus = null;
    }

    const byId = new Map(data.nodes.map((n) => [n.id, n]));
    const els = [];
    for (const n of data.nodes) {
      if (!keep.has(n.id)) continue;
      const el = { group: "nodes", data: { ...n } };
      // In clusters, tasks and entries live inside their project's box.
      if (clusters && (n.kind === "task" || n.kind === "entry") && keep.has("project:" + n.project)) {
        el.data.parent = "project:" + n.project;
      }
      if (positions.has(n.id)) el.position = positions.get(n.id);
      els.push(el);
    }
    const FLIP = new Set(["waits", "waited", "in", "logged"]);
    for (const e of data.edges) {
      if (!keep.has(e.source) || !keep.has(e.target)) continue;
      // The box already says a task or entry belongs to its project.
      if (clusters && e.rel === "in") continue;
      if (clusters && e.rel === "logged" && byId.get(e.target)?.kind === "project") continue;
      const flip = !clusters && FLIP.has(e.rel);
      els.push({
        group: "edges",
        selectable: false,
        classes: flip ? "flipped" : "",
        data: {
          id: `${e.rel}:${e.source}>${e.target}`, rel: e.rel, weight: e.weight,
          source: flip ? e.target : e.source, target: flip ? e.source : e.target,
        },
      });
    }

    cy.batch(() => {
      cy.elements().remove();
      cy.add(els);
    });
    run(fresh);
    counts();
    const sel = selected && cy.getElementById(selected);
    if (sel && sel.nonempty()) {
      sel.select();
      highlight(sel);
    }
  }

  function run(fresh) {
    const opts = layoutMode() === "flow"
      ? {
          name: "dagre", rankDir: "LR", nodeSep: 14, rankSep: 90, edgeSep: 6,
          nodeDimensionsIncludeLabels: true, animate: !fresh, animationDuration: 400, fit: true, padding: 40,
        }
      : {
          name: "fcose", quality: "default", randomize: fresh || positions.size === 0,
          animate: !fresh, animationDuration: 500, fit: true, padding: 40,
          nodeDimensionsIncludeLabels: true, packComponents: true, nestingFactor: 0.08,
          gravity: 0.3, gravityCompound: 1.2, tilingPaddingVertical: 20, tilingPaddingHorizontal: 20,
          nodeRepulsion: (n) => (n.data("kind") === "entry" ? 1500 : 9000),
          idealEdgeLength: (e) =>
            ({ logged: 25, about: 140, owns: 160, uses: 160, touches: 130, waits: 70, waited: 70 })[e.data("rel")] || 100,
          edgeElasticity: (e) =>
            ({ logged: 0.9, about: 0.05, touches: 0.1, owns: 0.2, uses: 0.1 })[e.data("rel")] || 0.45,
        };
    cy.layout(opts).run();
  }

  function counts() {
    const k = (kind) => cy.nodes(`[kind='${kind}']`).length;
    const warn = cy.nodes("[warn > 0]").length;
    ui.count.textContent =
      `${k("project")} projects · ${k("task")} tasks · ${k("repo")} repos` +
      (k("environment") ? ` · ${k("environment")} environments` : "") +
      (k("service") ? ` · ${k("service")} services` : "") +
      (k("entry") ? ` · ${k("entry")} entries` : "") +
      (warn ? ` · ${warn} with warnings` : "") +
      (focus ? " · focused (double-click the background to clear)" : "");
  }

  // --- highlight, search, tooltip -------------------------------------------

  function neighbourhood(node) {
    let hood = node.closedNeighborhood();
    // A box's neighbourhood is its members and what they touch.
    if (node.isParent()) hood = hood.union(node.children()).union(node.children().closedNeighborhood());
    return hood.union(hood.parents());
  }

  function highlight(node) {
    cy.elements().removeClass("faded hl");
    if (!node) return applySearch();
    const hood = neighbourhood(node);
    cy.elements().not(hood).addClass("faded");
    hood.addClass("hl");
  }

  function applySearch() {
    const q = ui.search.value.trim().toLowerCase();
    cy.elements().removeClass("faded hl");
    if (!q) return cy.collection();
    const hits = cy.nodes().filter((n) =>
      [n.data("label"), n.data("title"), n.data("detail")].join(" ").toLowerCase().includes(q));
    cy.elements().not(hits.union(hits.parents())).addClass("faded");
    hits.addClass("hl");
    return hits;
  }

  cy.on("mouseover", "node", (ev) => {
    const n = ev.target.data();
    const kind = n.kind === "entry" ? `${n.label} · ${n.state}` : n.kind + (n.state ? " · " + n.state : "");
    const title = n.kind === "entry" ? n.title : n.label;
    const sub = n.kind !== "entry" && n.title && n.title !== n.label ? n.title : "";
    let html = `<div class="tip-kind">${esc(kind)}</div><div class="tip-title">${esc(title || "")}</div>`;
    if (sub) html += `<div>${esc(sub)}</div>`;
    if (SUBJECTS.has(n.kind) && n.detail) html += `<div class="muted">${esc(n.detail)}</div>`;
    if (n.warn) html += `<div class="warn">${n.warn} warning${n.warn > 1 ? "s" : ""}, see the panel</div>`;
    ui.tip.innerHTML = html;
    ui.tip.hidden = false;
  });
  cy.on("mouseout", "node", () => { ui.tip.hidden = true; });
  cy.on("mousemove", (ev) => {
    if (ui.tip.hidden) return;
    const p = ev.renderedPosition;
    ui.tip.style.left = p.x + 16 + "px";
    ui.tip.style.top = p.y + 16 + "px";
  });

  function esc(s) {
    return String(s).replace(/[&<>"']/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[ch]);
  }

  // --- detail panel ---------------------------------------------------------

  function detailURL(n) {
    const q = new URLSearchParams({ kind: n.kind });
    if (!SUBJECTS.has(n.kind)) q.set("project", n.project);
    if (n.kind === "task") q.set("task", n.label);
    if (SUBJECTS.has(n.kind)) q.set(n.kind, n.label);
    if (n.kind === "entry") q.set("file", n.file);
    return "/graph/node?" + q;
  }

  async function show(node) {
    selected = node.id();
    syncURL();
    highlight(node);
    ui.panel.hidden = false;
    ui.body.innerHTML = '<p class="muted">Loading…</p>';
    const res = await fetch(detailURL(node.data()));
    const html = await res.text();
    // A later click may have arrived while this one loaded.
    if (selected !== node.id()) return;
    ui.body.innerHTML = res.ok ? html : `<div class="error">${esc(html)}</div>`;
    ui.panel.scrollTop = 0;
  }

  function hide() {
    selected = null;
    ui.panel.hidden = true;
    cy.nodes().unselect();
    highlight(null);
    syncURL();
  }

  // Links in the panel that name a node select it here instead of leaving.
  ui.body.addEventListener("click", (ev) => {
    const a = ev.target.closest("a[data-node]");
    if (!a || ev.metaKey || ev.ctrlKey) return;
    let id = a.dataset.node;
    // Subject ids are case-folded in the graph; a panel link carries the name as written.
    if (SUBJECTS.has(id.slice(0, id.indexOf(":")))) id = id.toLowerCase();
    const node = cy.getElementById(id);
    if (node.empty()) return; // filtered out of the picture: follow the link
    ev.preventDefault();
    cy.nodes().unselect();
    node.select();
    cy.animate({ center: { eles: node }, duration: 300 });
    show(node);
  });
  ui.close.addEventListener("click", hide);

  // --- interaction ----------------------------------------------------------

  // Cytoscape has no double-tap event; two taps on one target inside 350 ms are one.
  let lastTap = { id: null, t: 0 };
  cy.on("tap", (ev) => {
    // An edge has no detail of its own; a tap on one is neither a node nor the background.
    if (ev.target !== cy && ev.target.isEdge()) return;
    const id = ev.target === cy ? "bg" : ev.target.id();
    const now = Date.now();
    const double = lastTap.id === id && now - lastTap.t < 350;
    lastTap = double ? { id: null, t: 0 } : { id, t: now };

    if (id === "bg") {
      if (double && focus) {
        focus = null;
        rebuild(false);
      } else if (!double) {
        hide();
      }
      return;
    }
    if (double) {
      focus = id;
      rebuild(false);
      return;
    }
    show(ev.target);
  });

  ui.search.addEventListener("input", () => { if (!selected) applySearch(); });
  ui.search.addEventListener("keydown", (ev) => {
    if (ev.key !== "Enter") return;
    const hits = applySearch();
    if (hits.nonempty()) cy.animate({ fit: { eles: hits, padding: 80 }, duration: 400 });
    if (hits.length === 1) show(hits[0]);
  });
  ui.fit.addEventListener("click", () => cy.animate({ fit: { padding: 40 }, duration: 300 }));
  ui.project.addEventListener("change", () => {
    focus = null;
    selected = null;
    ui.panel.hidden = true;
    load();
  });
  ui.layout.addEventListener("change", () => { positions.clear(); syncURL(); rebuild(true); });
  ui.entries.addEventListener("change", () => { syncURL(); rebuild(false); });
  ui.closed.addEventListener("change", () => rebuild(false));
  document.addEventListener("keydown", (ev) => { if (ev.key === "Escape") hide(); });

  // The view's state lives in the URL, so a picture can be bookmarked or sent.
  function syncURL() {
    const url = new URL(location.href);
    const set = (k, v) => (v ? url.searchParams.set(k, v) : url.searchParams.delete(k));
    set("project", ui.project.value);
    set("layout", ui.layout.value === "auto" ? "" : ui.layout.value);
    set("entries", ui.entries.checked ? "1" : "");
    set("node", selected || "");
    history.replaceState(null, "", url);
  }

  const params = new URLSearchParams(location.search);
  if (params.get("entries") === "1") ui.entries.checked = true;
  if (["clusters", "flow"].includes(params.get("layout"))) ui.layout.value = params.get("layout");
  const wanted = params.get("node");

  load().then(() => {
    const node = wanted && cy.getElementById(wanted);
    if (node && node.nonempty()) {
      node.select();
      show(node);
    }
  });
})();
