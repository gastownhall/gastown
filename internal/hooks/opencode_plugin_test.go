package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	openCodeHookHelperEnv = "GO_WANT_OPENCODE_HOOK_HELPER"
	openCodeHookHangEnv   = "GO_OPENCODE_HOOK_HELPER_HANG"
	openCodeHookLogEnv    = "GO_OPENCODE_HOOK_HELPER_LOG"
)

func TestMain(m *testing.M) {
	if os.Getenv(openCodeHookHelperEnv) == "1" {
		os.Exit(runOpenCodeHookHelper())
	}
	os.Exit(m.Run())
}

func runOpenCodeHookHelper() int {
	args := os.Args[1:]
	if logPath := os.Getenv(openCodeHookLogEnv); logPath != "" {
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_, writeErr := fmt.Fprintf(file, "%s|source=%s|session=%s\n",
			strings.Join(args, " "), os.Getenv("GT_HOOK_SOURCE"), os.Getenv("GT_SESSION_ID"))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			fmt.Fprintln(os.Stderr, "writing hook helper log failed")
			return 2
		}
	}

	if len(args) >= 1 && args[0] == "prime" {
		if os.Getenv(openCodeHookHangEnv) == "1" {
			time.Sleep(time.Minute)
		}
		fmt.Printf("HOOK_CONTEXT source=%s session=%s\n", os.Getenv("GT_HOOK_SOURCE"), os.Getenv("GT_SESSION_ID"))
	}
	return 0
}

func TestOpenCodePluginLifecycleIsSessionScoped(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is required for the OpenCode plugin behavior test")
	}

	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "gastown.js")
	plugin, err := resolveAndSubstitute("opencode", "gastown.js", "crew")
	if err != nil {
		t.Fatalf("resolve OpenCode plugin: %v", err)
	}
	if err := os.WriteFile(pluginPath, plugin, 0644); err != nil {
		t.Fatalf("write OpenCode plugin: %v", err)
	}

	harnessPath := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(harnessPath, []byte(openCodePluginHarness), 0644); err != nil {
		t.Fatalf("write OpenCode plugin harness: %v", err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	logPath := filepath.Join(dir, "invocations.log")

	cmd := exec.Command(bun, harnessPath, pluginPath, testBinary, logPath, dir)
	cmd.Env = append(os.Environ(),
		openCodeHookHelperEnv+"=1",
		openCodeHookLogEnv+"="+logPath,
		"GT_ROLE=testrig/crew/alice",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("OpenCode plugin behavior test failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "OpenCode plugin behavior test passed") {
		t.Fatalf("OpenCode plugin behavior test did not complete:\n%s", output)
	}
}

const openCodePluginHarness = `
import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";

const [pluginPath, helperPath, logPath, workDir] = process.argv.slice(2);
process.env.GT_BIN = helperPath;
process.env.GO_OPENCODE_HOOK_HELPER_LOG = logPath;

const { GasTown } = await import(pathToFileURL(pluginPath).href);
const plugin = await GasTown({ directory: workDir });

const create = async (sessionID) => {
  await plugin.event({ event: { type: "session.created", properties: { sessionID } } });
};
const transform = async (sessionID) => {
  const output = { system: [] };
  await plugin["experimental.chat.system.transform"]({ sessionID }, output);
  return output.system;
};

await create("ses-a");
await create("ses-b");
assert.deepEqual(await transform("ses-a"), ["HOOK_CONTEXT source=startup session=ses-a\n"]);
assert.deepEqual(await transform("ses-b"), ["HOOK_CONTEXT source=startup session=ses-b\n"]);
assert.deepEqual(await transform("ses-a"), ["HOOK_CONTEXT source=startup session=ses-a\n"]);

await plugin.event({ event: { type: "session.compacted", properties: { sessionID: "ses-b" } } });
assert.deepEqual(await transform("ses-b"), ["HOOK_CONTEXT source=compact session=ses-b\n"]);

await plugin.event({ event: { type: "session.deleted", properties: { sessionID: "ses-a" } } });
assert.deepEqual(await transform("ses-a"), ["HOOK_CONTEXT source=startup session=ses-a\n"]);

const invocations = (await Bun.file(logPath).text()).trim().split("\n");
assert.equal(invocations.filter((line) => line === "prime --hook|source=startup|session=ses-a").length, 2);
assert.equal(invocations.filter((line) => line === "prime --hook|source=startup|session=ses-b").length, 1);
assert.equal(invocations.filter((line) => line === "prime --hook|source=compact|session=ses-b").length, 1);
assert.equal(invocations.some((line) => line.includes("costs record")), false);

const timeoutPluginPath = logPath + ".timeout-plugin.js";
const source = await Bun.file(pluginPath).text();
const timeoutSource = source.replace("const commandTimeoutMs = 10_000;", "const commandTimeoutMs = 100;");
assert.notEqual(timeoutSource, source);
await Bun.write(timeoutPluginPath, timeoutSource);
process.env.GO_OPENCODE_HOOK_HELPER_HANG = "1";
const { GasTown: TimeoutGasTown } = await import(pathToFileURL(timeoutPluginPath).href);
const timeoutPlugin = await TimeoutGasTown({ directory: workDir });
await timeoutPlugin.event({ event: { type: "session.created", properties: { sessionID: "ses-timeout" } } });
const started = performance.now();
assert.deepEqual(await (async () => {
  const output = { system: [] };
  await timeoutPlugin["experimental.chat.system.transform"]({ sessionID: "ses-timeout" }, output);
  return output.system;
})(), []);
delete process.env.GO_OPENCODE_HOOK_HELPER_HANG;
assert.ok(performance.now() - started < 3000, "hook timeout did not terminate the child promptly");

console.log("OpenCode plugin behavior test passed");
`
