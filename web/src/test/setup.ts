import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";
import { MockEventSource } from "./mocks";
import { resetApiState, timing } from "../api";

// jsdom's AbortSignal is not the one Node's undici Request accepts, and
// react-router builds a Request (with a signal) for every navigation. Drop
// the signal in tests so navigations work.
const NodeRequest = globalThis.Request;
class TestRequest extends NodeRequest {
  constructor(input: RequestInfo | URL, init?: RequestInit) {
    const { signal: _signal, ...rest } = init ?? {};
    super(input, rest);
  }
}
globalThis.Request = TestRequest as typeof Request;

beforeEach(() => {
  MockEventSource.instances = [];
  vi.stubGlobal("EventSource", MockEventSource);
  resetApiState();
  // Fast polling in tests; production values are in api.ts.
  timing.pollMs = 10;
  timing.pollMaxMs = 2000;
  timing.terminalMs = 5000;
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});
