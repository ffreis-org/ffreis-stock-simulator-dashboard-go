(() => {
  const boot = window.DASHBOARD_BOOT || { pollMs: 2000 };
  const pollMs = Number(boot.pollMs || 2000);

  const statusLine = byId("status-line");
  const errorLine = byId("error-line");
  const resultEl = byId("step-result");
  const chartCanvas = byId("equity-chart");

  const fields = {
    t: byId("state-t"),
    price: byId("state-price"),
    cash: byId("state-cash"),
    units: byId("state-units"),
    equity: byId("state-equity"),
    leverage: byId("state-leverage"),
    openOrders: byId("state-open-orders"),
    done: byId("state-done")
  };

  const sideField = byId("field-side");
  const unitsField = byId("field-units");
  const orderTypeField = byId("field-order-type");
  const limitField = byId("field-limit-price");

  const simDom = {
    tabBtnOverview: byId("tab-btn-overview"),
    tabBtnSimulations: byId("tab-btn-simulations"),
    tabOverview: byId("tab-overview"),
    tabSimulations: byId("tab-simulations"),
    capLine: byId("sim-cap-line"),
    errorLine: byId("sim-error-line"),
    btnRefresh: byId("btn-sim-refresh"),
    btnCapRefresh: byId("btn-sim-cap-refresh"),
    mainSummary: byId("sim-main-summary"),
    altSummary: byId("sim-alt-summary"),
    compareBody: byId("sim-compare-body"),
    compareChart: byId("sim-compare-chart"),
    flowsBody: byId("sim-flows-body"),
    sourceFlow: byId("field-source-flow"),
    rollback: byId("field-rollback-steps"),
    targetMainIndex: byId("field-target-main-index"),
    patchRows: byId("patch-rows"),
    btnAddPatch: byId("btn-add-patch"),
    branchForm: byId("sim-branch-form"),
    createResult: byId("sim-create-result"),
    activeFlow: byId("field-active-flow"),
    stepForm: byId("sim-step-form"),
    simSide: byId("sim-field-side"),
    simUnits: byId("sim-field-units"),
    simOrderType: byId("sim-field-order-type"),
    simLimit: byId("sim-field-limit-price"),
    stepResult: byId("sim-step-result"),
    traceResult: byId("sim-trace-result"),
    btnLoadTrace: byId("btn-load-trace")
  };

  const state = {
    history: [],
    maxPoints: 240,
    sim: {
      available: false,
      checkedAt: "",
      reason: "",
      flows: [],
      mainFlowId: "",
      selectedAltFlowIds: new Set(),
      sourceFlowId: "",
      activeFlowId: "",
      observations: new Map(),
      compareHistory: new Map()
    }
  };

  byId("btn-refresh").addEventListener("click", fetchState);
  byId("btn-reset").addEventListener("click", async () => {
    await callJSON("/api/reset", { seed: 1234 }, "POST");
    await fetchState();
  });

  byId("step-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const payload = buildOverviewStepPayload();
    const response = await callJSON("/api/step", payload, "POST");
    resultEl.textContent = JSON.stringify(response, null, 2);
    await fetchState();
  });

  sideField.addEventListener("change", syncOverviewFormState);
  orderTypeField.addEventListener("change", syncOverviewFormState);

  initTabs();
  initSim();
  syncOverviewFormState();
  syncSimFormState();
  fetchState();
  refreshSimCapabilities(false).then(() => {
    if (state.sim.available) {
      refreshSimFlows({ syncObservations: true });
    }
  });
  setInterval(() => {
    fetchState();
    pollSimSnapshot();
  }, pollMs);

  function byId(id) {
    return document.getElementById(id);
  }

  function initTabs() {
    const buttons = document.querySelectorAll(".tab-btn[data-tab]");
    buttons.forEach((button) => {
      button.addEventListener("click", () => {
        const tab = button.getAttribute("data-tab") || "overview";
        activateTab(tab);
      });
    });
  }

  function activateTab(tabName) {
    const targetIsSim = tabName === "simulations";
    simDom.tabBtnOverview.classList.toggle("active", !targetIsSim);
    simDom.tabBtnSimulations.classList.toggle("active", targetIsSim);
    simDom.tabOverview.classList.toggle("active", !targetIsSim);
    simDom.tabSimulations.classList.toggle("active", targetIsSim);
  }

  function initSim() {
    simDom.btnRefresh.addEventListener("click", async () => {
      await refreshSimFlows({ syncObservations: true, manual: true });
    });

    simDom.btnCapRefresh.addEventListener("click", async () => {
      await refreshSimCapabilities(true);
      if (state.sim.available) {
        await refreshSimFlows({ syncObservations: true, manual: true });
      }
    });

    simDom.sourceFlow.addEventListener("change", () => {
      state.sim.sourceFlowId = simDom.sourceFlow.value;
      updateTargetMainIndexPreview();
    });

    simDom.rollback.addEventListener("input", updateTargetMainIndexPreview);

    simDom.btnAddPatch.addEventListener("click", () => {
      addPatchRow();
    });

    simDom.patchRows.addEventListener("click", (event) => {
      const removeBtn = event.target.closest("button[data-remove-patch]");
      if (!removeBtn) {
        return;
      }
      const row = removeBtn.closest(".patch-row");
      if (row) {
        row.remove();
      }
    });

    simDom.patchRows.addEventListener("change", (event) => {
      const row = event.target.closest(".patch-row");
      if (!row) {
        return;
      }
      const sideInput = row.querySelector("[data-field='side']");
      const orderInput = row.querySelector("[data-field='order_type']");
      const unitsInput = row.querySelector("[data-field='units']");
      const limitInput = row.querySelector("[data-field='limit_price']");
      if (!sideInput || !orderInput || !unitsInput || !limitInput) {
        return;
      }
      if (sideInput.value === "hold") {
        unitsInput.value = "0";
        orderInput.value = "market";
        limitInput.value = "";
      }
      limitInput.disabled = orderInput.value !== "limit" || sideInput.value === "hold";
    });

    simDom.branchForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!state.sim.available) {
        return;
      }
      const source = findFlow(state.sim.sourceFlowId);
      if (!source) {
        setSimError("Select a source flow first.");
        return;
      }
      const rollbackSteps = Math.max(1, Math.trunc(Number(simDom.rollback.value || 1)));
      const targetMainIndex = computeTargetMainIndex(source.mainStepIndex, rollbackSteps);
      const patchRows = collectPatchRows();
      const payload = buildBranchPayload(source, rollbackSteps, targetMainIndex, patchRows);
      const response = await callJSON("/api/sim/branches", payload, "POST");
      simDom.createResult.textContent = JSON.stringify(response, null, 2);
      if (!response.error) {
        await refreshSimFlows({ syncObservations: true, manual: true });
      }
    });

    simDom.activeFlow.addEventListener("change", () => {
      state.sim.activeFlowId = simDom.activeFlow.value;
      renderComparisonPanels();
    });

    simDom.simSide.addEventListener("change", syncSimFormState);
    simDom.simOrderType.addEventListener("change", syncSimFormState);

    simDom.stepForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!state.sim.available) {
        return;
      }
      const activeFlow = stringsafe(simDom.activeFlow.value);
      if (!activeFlow) {
        setSimError("Select an active flow before stepping.");
        return;
      }

      const action = encodeAction(
        simDom.simSide.value,
        Number(simDom.simUnits.value || 0),
        simDom.simOrderType.value,
        simDom.simLimit.value.trim()
      );

      const payload = { actions: [action] };
      const response = await callJSON(`/api/sim/flows/${encodeURIComponent(activeFlow)}/step_many`, payload, "POST");
      simDom.stepResult.textContent = JSON.stringify(response, null, 2);
      if (!response.error) {
        await refreshObservation(activeFlow);
        await refreshSimFlows({ syncObservations: false });
      }
    });

    simDom.btnLoadTrace.addEventListener("click", async () => {
      if (!state.sim.available) {
        return;
      }
      const activeFlow = stringsafe(simDom.activeFlow.value);
      if (!activeFlow) {
        setSimError("Select an active flow before loading trace.");
        return;
      }
      const response = await callJSON(`/api/sim/flows/${encodeURIComponent(activeFlow)}/trace`, undefined, "GET");
      simDom.traceResult.textContent = JSON.stringify(response, null, 2);
    });

    addPatchRow();
  }

  function buildOverviewStepPayload() {
    const side = sideField.value;
    const orderType = orderTypeField.value;
    const units = Number(unitsField.value || 0);
    const limitRaw = limitField.value.trim();

    return {
      side,
      units,
      order_type: orderType,
      limit_price: limitRaw === "" ? null : Number(limitRaw)
    };
  }

  async function pollSimSnapshot() {
    await refreshSimCapabilities(false);
    if (!state.sim.available) {
      return;
    }
    await refreshSimFlows({ syncObservations: true });
  }

  function syncOverviewFormState() {
    const side = sideField.value;
    const isHold = side === "hold";
    const isLimit = orderTypeField.value === "limit";

    unitsField.disabled = isHold;
    orderTypeField.disabled = isHold;
    if (isHold) {
      unitsField.value = "0";
      orderTypeField.value = "market";
      limitField.value = "";
      limitField.disabled = true;
      return;
    }

    limitField.disabled = !isLimit;
    if (!isLimit) {
      limitField.value = "";
    }
  }

  function syncSimFormState() {
    const side = simDom.simSide.value;
    const isHold = side === "hold";
    const isLimit = simDom.simOrderType.value === "limit";

    simDom.simUnits.disabled = isHold;
    simDom.simOrderType.disabled = isHold;
    if (isHold) {
      simDom.simUnits.value = "0";
      simDom.simOrderType.value = "market";
      simDom.simLimit.value = "";
      simDom.simLimit.disabled = true;
      return;
    }

    simDom.simLimit.disabled = !isLimit;
    if (!isLimit) {
      simDom.simLimit.value = "";
    }
  }

  async function callJSON(url, payload, method = "POST") {
    const init = { method, headers: {} };
    if (payload !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(payload);
    }

    const response = await fetch(url, init);
    const text = await response.text();
    let parsed;
    try {
      parsed = text ? JSON.parse(text) : {};
    } catch {
      parsed = { raw: text };
    }

    if (!response.ok && url.startsWith("/api/") && !url.startsWith("/api/sim")) {
      errorLine.textContent = `Request failed (${response.status}): ${text}`;
    } else if (response.ok && !url.startsWith("/api/sim")) {
      errorLine.textContent = "";
    }

    if (!response.ok) {
      parsed = parsed || {};
      if (!parsed.error) {
        parsed.error = text || `request failed (${response.status})`;
      }
      parsed.status = response.status;
    }
    return parsed;
  }

  async function fetchState() {
    try {
      const response = await fetch("/api/state");
      const payload = await response.json();

      if (!response.ok) {
        statusLine.className = "status-line err";
        statusLine.textContent = "Simulator unreachable";
        errorLine.textContent = payload.last_error || "request failed";
        if (payload.sim_capabilities) {
          applySimCapabilityStatus(payload.sim_capabilities);
        }
        return;
      }

      renderState(payload);
      if (payload.sim_capabilities) {
        applySimCapabilityStatus(payload.sim_capabilities);
      }
    } catch (err) {
      statusLine.className = "status-line err";
      statusLine.textContent = "Network error";
      errorLine.textContent = String(err);
    }
  }

  function renderState(payload) {
    const ready = payload.readyz || {};
    if (ready.status === "ready" && ready.engine_ready) {
      statusLine.className = "status-line ok";
      statusLine.textContent = "Ready";
    } else {
      statusLine.className = "status-line warn";
      statusLine.textContent = "Not ready";
    }

    errorLine.textContent = payload.last_error || "";

    const obs = payload.observation;
    if (!obs) {
      setStateValues(null);
      return;
    }

    const market = obs.market_window_handle || {};
    const portfolio = obs.portfolio_vector || [];
    const orderSummary = obs.order_summary_vector || [];

    const row = {
      t: market.t,
      price: market.current_price,
      cash: portfolio[0],
      units: portfolio[1],
      equity: portfolio[2],
      leverage: portfolio[3],
      openOrders: orderSummary[0],
      done: obs.done
    };

    setStateValues(row);

    if (Number.isFinite(row.equity)) {
      state.history.push({ t: row.t, equity: row.equity });
      if (state.history.length > state.maxPoints) {
        state.history.shift();
      }
      drawLineChart(chartCanvas, [{ id: "main", points: state.history, color: "#0f766e" }], "equity");
    }
  }

  function setStateValues(row) {
    if (!row) {
      fields.t.textContent = "-";
      fields.price.textContent = "-";
      fields.cash.textContent = "-";
      fields.units.textContent = "-";
      fields.equity.textContent = "-";
      fields.leverage.textContent = "-";
      fields.openOrders.textContent = "-";
      fields.done.textContent = "-";
      return;
    }

    fields.t.textContent = formatInt(row.t);
    fields.price.textContent = formatNum(row.price);
    fields.cash.textContent = formatNum(row.cash);
    fields.units.textContent = formatNum(row.units);
    fields.equity.textContent = formatNum(row.equity);
    fields.leverage.textContent = formatNum(row.leverage, 4);
    fields.openOrders.textContent = formatInt(row.openOrders);
    fields.done.textContent = String(!!row.done);
  }

  async function refreshSimCapabilities(force) {
    const query = force ? "?refresh=1" : "";
    const payload = await callJSON(`/api/sim/capabilities${query}`, undefined, "GET");
    applySimCapabilityStatus(payload);
    return payload;
  }

  function applySimCapabilityStatus(payload) {
    const available = !!payload.available;
    const reason = stringsafe(payload.reason);
    const checkedAt = stringsafe(payload.checked_at);

    state.sim.available = available;
    state.sim.reason = reason;
    state.sim.checkedAt = checkedAt;

    const checkedSuffix = checkedAt ? ` (checked ${checkedAt})` : "";
    if (available) {
      simDom.capLine.className = "status-line ok";
      simDom.capLine.textContent = `Available${checkedSuffix}`;
      simDom.errorLine.textContent = "";
      setSimControlsEnabled(true);
      return;
    }

    simDom.capLine.className = "status-line warn";
    simDom.capLine.textContent = `Not available${checkedSuffix}`;
    simDom.errorLine.textContent = reason || "Simulation branch endpoints are unavailable upstream.";
    setSimControlsEnabled(false);
  }

  function setSimControlsEnabled(enabled) {
    simDom.tabSimulations.classList.toggle("sim-disabled", !enabled);

    const controls = simDom.tabSimulations.querySelectorAll(
      "button, input, select, textarea"
    );

    controls.forEach((el) => {
      if (el.id === "btn-sim-cap-refresh") {
        el.disabled = false;
        return;
      }
      el.disabled = !enabled;
    });

    if (!enabled) {
      simDom.flowsBody.innerHTML = "";
      simDom.compareBody.innerHTML = "";
      simDom.mainSummary.textContent = "Not available";
      simDom.altSummary.textContent = "Not available";
      drawLineChart(simDom.compareChart, [], "equity");
    }
  }

  async function refreshSimFlows(opts = {}) {
    if (!state.sim.available) {
      return;
    }

    const payload = await callJSON("/api/sim/flows", undefined, "GET");
    if (payload.error) {
      setSimError(payload.error);
      return;
    }

    clearSimError();
    const flows = normalizeFlows(payload);
    state.sim.flows = flows;
    state.sim.mainFlowId = chooseMainFlowId(flows);

    keepSelectionsConsistent();
    renderFlowSelectors();
    renderFlowsTable();
    updateTargetMainIndexPreview();

    if (opts.syncObservations) {
      await refreshSelectedObservations();
    }
    renderComparisonPanels();
  }

  function chooseMainFlowId(flows) {
    const main = flows.find((flow) => flow.flowType === "main");
    if (main) {
      return main.flowId;
    }
    return flows.length > 0 ? flows[0].flowId : "";
  }

  function keepSelectionsConsistent() {
    const ids = new Set(state.sim.flows.map((flow) => flow.flowId));

    state.sim.selectedAltFlowIds.forEach((id) => {
      if (!ids.has(id)) {
        state.sim.selectedAltFlowIds.delete(id);
      }
    });

    if (!ids.has(state.sim.sourceFlowId)) {
      state.sim.sourceFlowId = state.sim.mainFlowId;
    }
    if (!ids.has(state.sim.activeFlowId)) {
      state.sim.activeFlowId = state.sim.sourceFlowId || state.sim.mainFlowId;
    }
  }

  function renderFlowSelectors() {
    renderSelect(simDom.sourceFlow, state.sim.flows, state.sim.sourceFlowId || state.sim.mainFlowId);
    state.sim.sourceFlowId = simDom.sourceFlow.value;

    renderSelect(simDom.activeFlow, state.sim.flows, state.sim.activeFlowId || state.sim.mainFlowId);
    state.sim.activeFlowId = simDom.activeFlow.value;
  }

  function renderSelect(select, flows, selectedId) {
    const previous = selectedId || "";
    const options = flows.map((flow) => {
      const labelPrefix = flow.flowType === "main" ? "[main]" : "[alt]";
      const label = `${labelPrefix} ${flow.flowId}`;
      return `<option value="${escapeHTML(flow.flowId)}">${escapeHTML(label)}</option>`;
    });

    select.innerHTML = options.join("");
    if (!previous) {
      return;
    }
    const hasPrevious = flows.some((flow) => flow.flowId === previous);
    if (hasPrevious) {
      select.value = previous;
    }
  }

  function renderFlowsTable() {
    if (state.sim.flows.length === 0) {
      simDom.flowsBody.innerHTML = "<tr><td colspan=\"9\">No flows returned.</td></tr>";
      return;
    }

    const rows = state.sim.flows.map((flow) => {
      const isMain = flow.flowId === state.sim.mainFlowId;
      const checked = state.sim.selectedAltFlowIds.has(flow.flowId) ? "checked" : "";
      const compareCell = isMain
        ? "<span class=\"pill tiny\">baseline</span>"
        : `<input type=\"checkbox\" data-select-alt=\"${escapeHTML(flow.flowId)}\" ${checked} />`;
      const deleteButton = isMain
        ? "<button type=\"button\" disabled>Delete</button>"
        : `<button type=\"button\" data-delete-flow=\"${escapeHTML(flow.flowId)}\">Delete</button>`;

      const rangeStart = flow.anchorStart;
      const rangeEnd = flow.anchorEnd;
      const rangeLabel =
        Number.isFinite(rangeStart) && Number.isFinite(rangeEnd)
          ? `${formatInt(rangeStart)}..${formatInt(rangeEnd)}`
          : "-";
      const anchorLabel = Number.isFinite(flow.mainStepIndex) ? formatInt(flow.mainStepIndex) : "-";

      return `
        <tr>
          <td>${compareCell}</td>
          <td><code>${escapeHTML(flow.flowId)}</code></td>
          <td><code>${escapeHTML(shortHash(flow.hash))}</code></td>
          <td>${escapeHTML(flow.flowType || "-")}</td>
          <td><code>${escapeHTML(flow.parentFlowId || "-")}</code></td>
          <td>${anchorLabel}</td>
          <td>${rangeLabel}</td>
          <td>${escapeHTML(flow.status || "-")}</td>
          <td>${deleteButton}</td>
        </tr>
      `;
    });

    simDom.flowsBody.innerHTML = rows.join("");

    simDom.flowsBody.querySelectorAll("input[data-select-alt]").forEach((checkbox) => {
      checkbox.addEventListener("change", async (event) => {
        const flowId = event.target.getAttribute("data-select-alt");
        if (!flowId) {
          return;
        }
        if (event.target.checked) {
          state.sim.selectedAltFlowIds.add(flowId);
          await refreshObservation(flowId);
        } else {
          state.sim.selectedAltFlowIds.delete(flowId);
        }
        renderComparisonPanels();
      });
    });

    simDom.flowsBody.querySelectorAll("button[data-delete-flow]").forEach((button) => {
      button.addEventListener("click", async () => {
        const flowId = button.getAttribute("data-delete-flow");
        if (!flowId) {
          return;
        }
        if (!window.confirm(`Delete alternative flow ${flowId}?`)) {
          return;
        }
        const response = await callJSON(`/api/sim/flows/${encodeURIComponent(flowId)}`, undefined, "DELETE");
        simDom.stepResult.textContent = JSON.stringify(response, null, 2);
        if (!response.error) {
          state.sim.observations.delete(flowId);
          state.sim.compareHistory.delete(flowId);
          state.sim.selectedAltFlowIds.delete(flowId);
          await refreshSimFlows({ syncObservations: true, manual: true });
        }
      });
    });
  }

  async function refreshSelectedObservations() {
    const targetIDs = new Set();
    if (state.sim.mainFlowId) {
      targetIDs.add(state.sim.mainFlowId);
    }
    state.sim.selectedAltFlowIds.forEach((id) => targetIDs.add(id));
    if (state.sim.activeFlowId) {
      targetIDs.add(state.sim.activeFlowId);
    }

    const ids = Array.from(targetIDs);
    await Promise.all(ids.map((id) => refreshObservation(id)));
  }

  async function refreshObservation(flowID) {
    if (!flowID) {
      return;
    }
    const payload = await callJSON(`/api/sim/flows/${encodeURIComponent(flowID)}/observe`, undefined, "GET");
    if (payload.error) {
      return;
    }
    const metrics = extractObservationMetrics(payload);
    state.sim.observations.set(flowID, metrics);

    if (Number.isFinite(metrics.equity)) {
      const history = state.sim.compareHistory.get(flowID) || [];
      history.push({ t: metrics.t, equity: metrics.equity });
      if (history.length > 240) {
        history.shift();
      }
      state.sim.compareHistory.set(flowID, history);
    }
  }

  function renderComparisonPanels() {
    const mainFlow = findFlow(state.sim.mainFlowId);
    const focusAlt = findFlow(state.sim.activeFlowId);
    const mainMetrics = mainFlow ? state.sim.observations.get(mainFlow.flowId) : null;
    const altMetrics = focusAlt ? state.sim.observations.get(focusAlt.flowId) : null;

    simDom.mainSummary.textContent = summarizeFlow(mainFlow, mainMetrics);
    simDom.altSummary.textContent = summarizeFlow(focusAlt, altMetrics);

    renderComparisonTable(mainFlow, mainMetrics);
    renderComparisonChart(mainFlow);
  }

  function renderComparisonTable(mainFlow, mainMetrics) {
    const selectedAltIDs = Array.from(state.sim.selectedAltFlowIds);
    const rows = [];

    if (mainFlow) {
      rows.push(renderCompareRow(mainFlow, mainMetrics, 0));
    }

    selectedAltIDs.forEach((flowID) => {
      const flow = findFlow(flowID);
      if (!flow) {
        return;
      }
      const metrics = state.sim.observations.get(flow.flowId);
      const deltaEquity = Number.isFinite(metrics?.equity) && Number.isFinite(mainMetrics?.equity)
        ? metrics.equity - mainMetrics.equity
        : NaN;
      rows.push(renderCompareRow(flow, metrics, deltaEquity));
    });

    if (rows.length === 0) {
      simDom.compareBody.innerHTML = "<tr><td colspan=\"7\">Select alternative flows to compare.</td></tr>";
      return;
    }

    simDom.compareBody.innerHTML = rows.join("");
  }

  function renderCompareRow(flow, metrics, deltaEquity) {
    return `
      <tr>
        <td><code>${escapeHTML(flow.flowId)}</code></td>
        <td>${escapeHTML(flow.status || "-")}</td>
        <td>${formatInt(metrics?.t)}</td>
        <td>${formatNum(metrics?.equity)}</td>
        <td>${formatSigned(deltaEquity)}</td>
        <td>${formatNum(metrics?.reward)}</td>
        <td>${formatInt(metrics?.fills)}</td>
      </tr>
    `;
  }

  function renderComparisonChart(mainFlow) {
    if (!mainFlow) {
      drawLineChart(simDom.compareChart, [], "equity");
      return;
    }

    const lines = [];
    const mainHistory = state.sim.compareHistory.get(mainFlow.flowId) || [];
    lines.push({ id: mainFlow.flowId, points: mainHistory, color: "#1d4ed8" });

    Array.from(state.sim.selectedAltFlowIds).forEach((flowID, index) => {
      const points = state.sim.compareHistory.get(flowID) || [];
      const color = compareColor(index);
      lines.push({ id: flowID, points, color });
    });

    drawLineChart(simDom.compareChart, lines, "equity");
  }

  function compareColor(index) {
    const palette = ["#dc2626", "#7c3aed", "#0891b2", "#ea580c", "#059669", "#475569"];
    return palette[index % palette.length];
  }

  function summarizeFlow(flow, metrics) {
    if (!flow) {
      return "-";
    }
    const equity = formatNum(metrics?.equity);
    const reward = formatNum(metrics?.reward);
    const pnl = formatNum(metrics?.cumulativePnl);
    return `${flow.flowId} | status=${flow.status || "-"} | equity=${equity} | reward=${reward} | cumulative-pnl=${pnl}`;
  }

  function updateTargetMainIndexPreview() {
    const source = findFlow(state.sim.sourceFlowId || simDom.sourceFlow.value);
    if (!source || !Number.isFinite(source.mainStepIndex)) {
      simDom.targetMainIndex.value = "-";
      return;
    }
    const rollback = Math.max(1, Math.trunc(Number(simDom.rollback.value || 1)));
    const target = computeTargetMainIndex(source.mainStepIndex, rollback);
    simDom.targetMainIndex.value = String(target);
  }

  function computeTargetMainIndex(sourceMainIndex, rollbackSteps) {
    const safeMain = Number.isFinite(sourceMainIndex) ? Math.trunc(sourceMainIndex) : 0;
    const safeRollback = Math.max(0, Math.trunc(rollbackSteps || 0));
    const target = safeMain - safeRollback;
    return target < 0 ? 0 : target;
  }

  function addPatchRow(seed = {}) {
    const row = document.createElement("div");
    row.className = "patch-row";
    row.innerHTML = `
      <label>
        Step Offset
        <input type="number" min="0" step="1" data-field="step_offset" value="${escapeHTML(String(seed.step_offset || 0))}" />
      </label>
      <label>
        Side
        <select data-field="side">
          <option value="hold">hold</option>
          <option value="buy">buy</option>
          <option value="sell">sell</option>
        </select>
      </label>
      <label>
        Units
        <input type="number" min="0" step="0.01" data-field="units" value="${escapeHTML(String(seed.units || 0))}" />
      </label>
      <label>
        Order Type
        <select data-field="order_type">
          <option value="market">market</option>
          <option value="limit">limit</option>
        </select>
      </label>
      <label>
        Limit Price
        <input type="number" step="0.0001" data-field="limit_price" value="${escapeHTML(String(seed.limit_price || ""))}" />
      </label>
      <button type="button" data-remove-patch="1">Remove</button>
    `;

    simDom.patchRows.appendChild(row);

    const sideInput = row.querySelector("[data-field='side']");
    const orderInput = row.querySelector("[data-field='order_type']");
    const limitInput = row.querySelector("[data-field='limit_price']");
    if (sideInput && seed.side) {
      sideInput.value = seed.side;
    }
    if (orderInput && seed.order_type) {
      orderInput.value = seed.order_type;
    }
    if (limitInput) {
      limitInput.disabled = orderInput.value !== "limit" || sideInput.value === "hold";
    }
  }

  function collectPatchRows() {
    const rows = [];
    simDom.patchRows.querySelectorAll(".patch-row").forEach((row) => {
      const side = row.querySelector("[data-field='side']")?.value || "hold";
      const units = Number(row.querySelector("[data-field='units']")?.value || 0);
      const orderType = row.querySelector("[data-field='order_type']")?.value || "market";
      const limitRaw = stringsafe(row.querySelector("[data-field='limit_price']")?.value);
      const stepOffset = Math.max(0, Math.trunc(Number(row.querySelector("[data-field='step_offset']")?.value || 0)));

      const action = {
        side,
        units,
        order_type: orderType,
        limit_price: limitRaw === "" ? null : Number(limitRaw)
      };
      const encodedAction = encodeAction(side, units, orderType, limitRaw);

      rows.push({
        step_offset: stepOffset,
        action,
        encoded_action: encodedAction
      });
    });
    return rows;
  }

  function buildBranchPayload(sourceFlow, rollbackSteps, targetMainIndex, patchRows) {
    return {
      source_flow_id: sourceFlow.flowId,
      parent_flow_id: sourceFlow.flowId,
      rollback_steps: rollbackSteps,
      target_main_step_index: targetMainIndex,
      patch_mode: "decision_patch",
      decisions: patchRows,
      lineage: {
        anchor_main_step_index: targetMainIndex,
        anchor_hash: sourceFlow.hash || "",
        parent_flow_id: sourceFlow.flowId
      }
    };
  }

  function encodeAction(side, units, orderType, limitRaw) {
    let sideCode = 0;
    if (side === "buy") {
      sideCode = 1;
    } else if (side === "sell") {
      sideCode = -1;
    }

    let orderTypeCode = orderType === "limit" ? 1 : 0;
    let hasLimitPrice = stringsafe(limitRaw) !== "";
    let limitPrice = hasLimitPrice ? Number(limitRaw) : null;

    if (side === "hold") {
      orderTypeCode = 0;
      hasLimitPrice = false;
      limitPrice = null;
      units = 0;
    }

    return {
      side_code: sideCode,
      units: side === "hold" ? 0 : Number(units || 0),
      order_type_code: orderTypeCode,
      has_limit_price: hasLimitPrice,
      limit_price: limitPrice
    };
  }

  function extractObservationMetrics(payload) {
    const observation = payload.observation || payload.observe?.observation || payload.state?.observation || {};
    const market = observation.market_window_handle || observation.marketWindowHandle || {};
    const portfolio = observation.portfolio_vector || observation.portfolioVector || [];

    const rewardCandidate = firstFinite([
      payload.reward,
      payload.last_reward,
      payload.step_result?.reward,
      payload.last_step?.reward
    ]);

    const fillsCandidate = firstFinite([
      countMaybe(payload.fills),
      countMaybe(payload.last_fills),
      payload.fill_count,
      payload.last_step?.fill_count
    ]);

    const cumulativePnlCandidate = firstFinite([
      payload.cumulative_pnl,
      payload.cumulativePnl,
      payload.step_result?.cumulative_pnl,
      payload.last_step?.cumulative_pnl
    ]);

    return {
      t: firstFinite([market.t, payload.t]),
      equity: firstFinite([portfolio[2], payload.equity, payload.state?.equity]),
      reward: rewardCandidate,
      fills: fillsCandidate,
      cumulativePnl: cumulativePnlCandidate,
      done: !!observation.done
    };
  }

  function normalizeFlows(payload) {
    const items = Array.isArray(payload)
      ? payload
      : Array.isArray(payload.flows)
        ? payload.flows
        : [];

    return items
      .map((raw) => normalizeFlow(raw))
      .filter((flow) => stringsafe(flow.flowId) !== "");
  }

  function normalizeFlow(raw) {
    const lineage = raw.lineage || {};
    const indices = raw.index_metadata || raw.indexes || {};
    const anchor = lineage.anchor || raw.anchor || {};

    const flowId = stringsafe(raw.flow_id || raw.flowId || raw.id);
    const flowType = stringsafe(raw.flow_type || raw.flowType || lineage.flow_type || (flowId === "main" ? "main" : "alternative"));
    const parentFlowId = stringsafe(raw.parent_flow_id || raw.parentFlowId || lineage.parent_flow_id || "");
    const hash = stringsafe(raw.hash || raw.flow_hash || lineage.hash || anchor.hash || "");

    return {
      raw,
      flowId,
      flowType,
      parentFlowId,
      hash,
      status: stringsafe(raw.status || lineage.status || anchor.status || ""),
      mainStepIndex: firstFinite([
        raw.main_step_index,
        raw.mainStepIndex,
        indices.main_step_index,
        indices.main,
        anchor.main_step_index
      ]),
      branchStepIndex: firstFinite([
        raw.branch_step_index,
        raw.branchStepIndex,
        indices.branch_step_index,
        indices.branch,
        anchor.branch_step_index
      ]),
      anchorStart: firstFinite([raw.start_index, anchor.start_index, anchor.start]),
      anchorEnd: firstFinite([raw.end_index, anchor.end_index, anchor.end]),
      anchorID: stringsafe(raw.anchor_id || anchor.id || ""),
      anchorHash: stringsafe(raw.anchor_hash || anchor.hash || "")
    };
  }

  function findFlow(flowID) {
    return state.sim.flows.find((flow) => flow.flowId === flowID) || null;
  }

  function drawLineChart(canvas, lines, valueField) {
    const ctx = canvas.getContext("2d");
    const width = canvas.width;
    const height = canvas.height;
    ctx.clearRect(0, 0, width, height);

    const allPoints = lines.flatMap((line) => line.points || []);
    if (allPoints.length < 2) {
      return;
    }

    const values = allPoints.map((point) => Number(point[valueField])).filter(Number.isFinite);
    if (values.length < 2) {
      return;
    }

    const minY = Math.min(...values);
    const maxY = Math.max(...values);
    const pad = (maxY - minY) * 0.1 || 1;

    const y0 = minY - pad;
    const y1 = maxY + pad;

    lines.forEach((line) => {
      const points = line.points || [];
      if (points.length < 2) {
        return;
      }
      ctx.strokeStyle = line.color || "#0f766e";
      ctx.lineWidth = 2;
      ctx.beginPath();

      points.forEach((point, index) => {
        const x = (index / Math.max(1, points.length - 1)) * (width - 20) + 10;
        const y = height - ((point[valueField] - y0) / (y1 - y0)) * (height - 20) - 10;
        if (index === 0) {
          ctx.moveTo(x, y);
        } else {
          ctx.lineTo(x, y);
        }
      });
      ctx.stroke();
    });
  }

  function shortHash(value) {
    const text = stringsafe(value);
    if (text.length <= 10) {
      return text || "-";
    }
    return `${text.slice(0, 10)}...`;
  }

  function escapeHTML(value) {
    const text = String(value ?? "");
    return text
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#39;");
  }

  function stringsafe(value) {
    return value == null ? "" : String(value).trim();
  }

  function firstFinite(values) {
    for (const value of values) {
      const n = Number(value);
      if (Number.isFinite(n)) {
        return n;
      }
    }
    return NaN;
  }

  function countMaybe(value) {
    if (Array.isArray(value)) {
      return value.length;
    }
    return value;
  }

  function setSimError(message) {
    simDom.errorLine.textContent = message || "";
  }

  function clearSimError() {
    simDom.errorLine.textContent = "";
  }

  function formatNum(value, digits = 2) {
    if (!Number.isFinite(value)) {
      return "-";
    }
    return Number(value).toLocaleString(undefined, {
      maximumFractionDigits: digits,
      minimumFractionDigits: digits
    });
  }

  function formatSigned(value) {
    if (!Number.isFinite(value)) {
      return "-";
    }
    const prefix = value > 0 ? "+" : "";
    return `${prefix}${formatNum(value)}`;
  }

  function formatInt(value) {
    if (!Number.isFinite(value)) {
      return "-";
    }
    return Math.trunc(value).toLocaleString();
  }

  if (window.__DASHBOARD_TEST__ === true) {
    window.__dashboardTestHooks = {
      buildBranchPayload,
      computeTargetMainIndex,
      normalizeFlows,
      extractObservationMetrics,
      encodeAction
    };
  }
})();
