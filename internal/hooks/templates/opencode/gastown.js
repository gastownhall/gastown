// Gas Town OpenCode plugin: hooks SessionStart/Compaction via events.
// Injects gt prime context into the system prompt via experimental.chat.system.transform.
//
// Compaction auto-cycling: After MAX_COMPACTIONS cycles, the plugin saves state
// (costs + handoff mail), kills the tmux session, and the daemon patrol detects
// the dead session and re-spawns the polecat with a fresh context window. This
// replaces Claude's native PreCompact → gt handoff --cycle hook chain for agents
// that cannot self-respawn from within their hook/plugin system.
export const GasTown = async ({ $, directory }) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  const gtBin = process.env.GT_BIN || "gt";
  let didInit = false;

  // Promise-based context loading ensures the system transform hook can
  // await the result even if session.created hasn't resolved yet.
  let primePromise = null;

  const outputTail = (value, maxChars = 2000) => {
    if (value === undefined || value === null) return "<empty>";
    let text;
    if (typeof value === "string") {
      text = value;
    } else if (value instanceof Uint8Array) {
      text = new TextDecoder().decode(value);
    } else {
      text = String(value);
    }
    text = text.trimEnd();
    if (!text) return "<empty>";
    if (text.length <= maxChars) return text;
    return `...<truncated ${text.length - maxChars} chars>\n${text.slice(-maxChars)}`;
  };

  const exitCode = (err) => {
    const candidates = [err?.exitCode, err?.exit_code, err?.status, err?.code];
    for (const code of candidates) {
      if (typeof code === "number") return code;
    }
    return null;
  };

  const shellQuote = (value) => `'${String(value).replace(/'/g, `'\\''`)}'`;

  const gtCommand = () => {
    if (/^[A-Za-z0-9_./-]+$/.test(gtBin)) return gtBin;
    return shellQuote(gtBin);
  };

  const isDoltBackedCommand = (cmd) =>
    /(^|\s)(?:'[^']*\/|[^'\s]*\/)?(?:gt|bd)'?\s/.test(cmd) &&
    !/(^|\s)(?:'[^']*\/|[^'\s]*\/)?gt'?\s+dolt\s+status(\s|$)/.test(cmd);

  const captureDoltStatus = async () => {
    const statusCmd = `timeout 10s ${gtCommand()} dolt status 2>&1`;
    try {
      return await $`/bin/sh -lc ${statusCmd}`.cwd(directory).text();
    } catch (err) {
      return [
        `status_command: ${statusCmd}`,
        `status_error: ${err?.message || err}`,
        `status_stdout_tail: ${outputTail(err?.stdout)}`,
        `status_stderr_tail: ${outputTail(err?.stderr)}`,
      ].join("\n");
    }
  };

  const logFailure = async (cmd, err) => {
    const code = exitCode(err);
    const message = err?.message || String(err);
    const timeout = code === 124 || /exit code 124|timed?\s*out/i.test(message)
      ? "yes (exit code 124 / timeout)"
      : "not indicated";
    const lines = [
      "[gastown] command failed",
      `command: ${cmd}`,
      `exit_code: ${code ?? "unknown"}`,
      `timeout: ${timeout}`,
      `error: ${message}`,
      `stdout_tail: ${outputTail(err?.stdout)}`,
      `stderr_tail: ${outputTail(err?.stderr)}`,
    ];
    if (isDoltBackedCommand(cmd)) {
      lines.push(`dolt_status_tail:\n${outputTail(await captureDoltStatus())}`);
      lines.push(
        "suggested_recovery: If Dolt is unhealthy or another gt/bd command is hanging, capture SIGQUIT and `gt dolt status` diagnostics before escalating; otherwise retry after the timeout clears."
      );
    } else {
      lines.push("suggested_recovery: Inspect the command, stdout/stderr tails, and retry once the timeout or process failure is resolved.");
    }
    console.error(lines.join("\n"));
  };

  const simpleRole = (value) => {
    if (!value) return "";
    const parts = value.split("/").filter(Boolean);
    if (parts.length >= 2 && parts[1] === "polecats") return "polecat";
    if (parts.length >= 2 && parts[1] === "crew") return "crew";
    if (parts.length >= 2) return parts[1];
    return parts[0];
  };

  const eventSessionID = (event) => event?.properties?.info?.id || event?.sessionID || event?.session?.id || "";

  // Compaction tracking for session auto-cycle (replaces Claude's PreCompact hook).
  // After MAX_COMPACTIONS, the plugin signals the daemon to restart the session.
  // This prevents context quality degradation for non-Claude agents that lack
  // Claude's native session-cycling hook (gt handoff --cycle).
  const MAX_COMPACTIONS = 3;
  let compactionCount = 0;
  let cycleSignalled = false;

  const captureRun = async (cmd) => {
    try {
      return await $`/bin/sh -lc ${cmd}`.cwd(directory).text();
    } catch (err) {
      await logFailure(cmd, err);
      return "";
    }
  };

  const loadPrime = async (source = "startup", sessionID = "") => {
    const env = [`GT_HOOK_SOURCE=${shellQuote(source)}`];
    if (sessionID) {
      env.push(`GT_SESSION_ID=${shellQuote(sessionID)}`);
    }
    let context = await captureRun(`${env.join(" ")} ${gtCommand()} prime --hook`);
    // NOTE: session-started nudge to deacon removed — it interrupted
    // the deacon's await-signal backoff. Deacon wakes on beads activity.
    return context;
  };

  const signalSessionCycle = async () => {
    if (cycleSignalled) return;
    cycleSignalled = true;
    const sessionName = process.env.GT_SESSION_NAME || "";
    // Save state before cycling: record costs and send handoff mail so the
    // next session inherits context. Uses --auto (save-only, no respawn)
    // because opencode cannot self-respawn via hooks.
    await captureRun(`${gtCommand()} costs record`);
    await captureRun(`${gtCommand()} handoff --auto -s ${shellQuote("OpenCode compaction cycle")} -m ${shellQuote(`Compacted ${compactionCount} times - context snapshot for successor`)}`);

    if (sessionName) {
      // Kill the tmux session to trigger daemon-driven restart. The deacon/witness
      // patrol detects the dead session, reads the handoff mail, and re-spawns
      // the polecat with a fresh context window. This replaces Claude's native
      // session-cycling PreCompact hook (gt handoff --cycle) for non-Claude agents.
      await captureRun(`tmux kill-session -t ${shellQuote(sessionName)}`);
      console.error(`[gastown] session cycle complete - killed ${sessionName} after ${compactionCount} compactions`);
    } else {
      console.error(`[gastown] session cycle signalled after ${compactionCount} compactions - waiting for daemon patrol`);
    }
  };

  return {
    event: async ({ event }) => {
      if (event?.type === "session.created") {
        if (didInit) return;
        didInit = true;
        compactionCount = 0;
        cycleSignalled = false;
        // Start loading prime context early; system.transform will await it.
        primePromise = loadPrime("startup", eventSessionID(event));
      }
      if (event?.type === "session.compacted") {
        compactionCount++;
        // Reset so next system.transform gets fresh context.
        primePromise = loadPrime("compact", eventSessionID(event));
        if (compactionCount >= MAX_COMPACTIONS) {
          await signalSessionCycle();
        }
      }
      if (event?.type === "session.deleted") {
        const sessionID = event.properties?.info?.id;
        if (sessionID) {
          await captureRun(`${gtCommand()} costs record --session ${shellQuote(sessionID)}`);
        }
      }
    },
    "experimental.chat.system.transform": async (input, output) => {
      if (!primePromise) {
        primePromise = loadPrime("startup");
      }
      const context = await primePromise;
      if (context) {
        output.system.push(context);
      } else {
        primePromise = null;
      }
    },
    "experimental.session.compacting": async ({ sessionID }, output) => {
      const roleDisplay = simpleRole(role) || "unknown";
      const willCycle = compactionCount + 1 >= MAX_COMPACTIONS;
      output.context.push(`
## Gas Town Multi-Agent System

**After Compaction:** Run \`gt prime --hook\` to restore full context.
**Check Hook:** \`gt hook\` - if work present, execute immediately (GUPP).
**Role:** ${roleDisplay}${willCycle ? `\n**Session Cycle:** Compaction limit reached (${compactionCount + 1}/${MAX_COMPACTIONS}). The daemon will restart this session after compaction to restore full context quality.` : ""}
`);
    },
  };
};
