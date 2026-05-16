// Gas Town OpenCode plugin: hooks SessionStart/Compaction via events.
// Injects gt prime context into the system prompt via experimental.chat.system.transform.
export const GasTown = async ({ $, directory }) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  const autonomousRoles = new Set(["polecat", "witness", "refinery", "deacon"]);
  let didInit = false;

  // Promise-based context loading ensures the system transform hook can
  // await the result even if session.created hasn't resolved yet.
  let primePromise = null;

  const captureRun = async (cmd) => {
    try {
      // .text() captures stdout as a string and suppresses terminal echo.
      return await $`/bin/sh -lc ${cmd}`.cwd(directory).text();
    } catch (err) {
      console.error(`[gastown] ${cmd} failed`, err?.message || err);
      return "";
    }
  };

  const loadPrime = async () => {
    let context = await captureRun("gt prime");
    if (autonomousRoles.has(role)) {
      const mail = await captureRun("gt mail check --inject");
      if (mail) {
        context += "\n" + mail;
      }
    }
    // NOTE: session-started nudge to deacon removed — it interrupted
    // the deacon's await-signal backoff. Deacon wakes on beads activity.
    return context;
  };

  return {
    event: async ({ event }) => {
      if (event?.type === "session.created") {
        if (didInit) return;
        didInit = true;
        // Start loading prime context early; system.transform will await it.
        primePromise = loadPrime();
      }
      if (event?.type === "session.compacted") {
        // Reset so next system.transform gets fresh context.
        primePromise = loadPrime();
      }
      if (event?.type === "session.deleted") {
        const sessionID = event.properties?.info?.id;
        if (sessionID) {
          // Pass cost from the event when available so gt costs record does not
          // attempt Claude-specific transcript parsing for opencode sessions.
          const cost = event.properties?.info?.cost;
          if (typeof cost === "number" && cost > 0) {
            await $`gt costs record --session ${sessionID} --cost ${cost.toFixed(6)}`.catch(() => {});
          } else {
            await $`gt costs record --session ${sessionID}`.catch(() => {});
          }
        }
      }
    },
    "experimental.chat.system.transform": async (input, output) => {
      // If session.created hasn't fired yet, start loading now.
      if (!primePromise) {
        primePromise = loadPrime();
      }
      const context = await primePromise;
      if (context) {
        output.system.push(context);
      } else {
        // Reset so next transform retries instead of pushing empty forever.
        primePromise = null;
      }
    },
    "experimental.session.compacting": async ({ sessionID }, output) => {
      const roleDisplay = role || "unknown";
      output.context.push(`
## Gas Town Multi-Agent System

**After Compaction:** Run \`gt prime\` to restore full context.
**Check Hook:** \`gt hook\` - if work present, execute immediately (GUPP).
**Role:** ${roleDisplay}
`);
    },
  };
};
