// @vitest-environment jsdom
import { act, createElement, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";

import { useAsyncData } from "../lib/use-async-data";

declare global {
  // React reads this off the global scope to decide whether `act` can flush
  // effects synchronously; without it the counts below would race.
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined;
}

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

/**
 * The behaviour the panels depend on is that the hook fires one request per
 * genuine change to its dependencies. The trap is that every caller passes a
 * fresh array literal (`[bankId, search]`), so keying the request on the array's
 * identity re-keys on every render and the fetch effect loops forever. These
 * cases render a component the way a panel does and count the requests.
 */

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

type ProbeState = { loading: boolean; data: unknown; error: boolean };

interface ProbeHarness {
  Probe: (props: { dep: number }) => null;
  root: Root;
  calls: () => number;
  pending: Array<{ resolve: (value: unknown) => void; reject: (reason?: unknown) => void }>;
  latest: () => ProbeState;
  bumpUnrelated: () => void;
  render: (dep: number) => Promise<void>;
  unmount: () => Promise<void>;
}

function mountProbe(): ProbeHarness {
  let calls = 0;
  const pending: Array<{ resolve: (value: unknown) => void; reject: (reason?: unknown) => void }> = [];
  let latest: ProbeState = { loading: true, data: null, error: false };
  let bumpUnrelated: () => void = () => {};

  function Probe({ dep }: { dep: number }) {
    const [, setN] = useState(0);
    bumpUnrelated = () => setN((value) => value + 1);
    // An inline array literal on purpose: this is what every panel passes.
    const state = useAsyncData(() => {
      calls += 1;
      const gate = deferred<unknown>();
      pending.push({ resolve: gate.resolve, reject: gate.reject });
      return gate.promise;
    }, ["fixed", dep]);
    latest = { loading: state.loading, data: state.data, error: state.error };
    return null;
  }

  const container = document.createElement("div");
  const root: Root = createRoot(container);

  return {
    root,
    Probe,
    calls: () => calls,
    pending,
    latest: () => latest,
    bumpUnrelated: () => bumpUnrelated(),
    render: (dep: number) => act(async () => root.render(createElement(Probe, { dep }))),
    unmount: () => act(async () => root.unmount()),
  };
}

const mounted: ProbeHarness[] = [];

afterEach(async () => {
  for (const probe of mounted.splice(0)) await probe.unmount();
});

function mount() {
  const probe = mountProbe();
  mounted.push(probe);
  return probe;
}

describe("useAsyncData request keying", () => {
  it("fires once for an inline deps literal and does not refetch on an unrelated re-render", async () => {
    const probe = mount();
    await probe.render(1);
    expect(probe.calls()).toBe(1);

    // A re-render that changes nothing the hook depends on must not refetch;
    // an identity-keyed request key makes this loop forever.
    await act(async () => probe.bumpUnrelated());
    await act(async () => probe.bumpUnrelated());
    expect(probe.calls()).toBe(1);

    probe.pending[0].resolve("first");
    await act(async () => {});
    expect(probe.latest().data).toBe("first");
    expect(probe.latest().loading).toBe(false);
  });

  it("refetches when a dependency value actually changes", async () => {
    const probe = mount();
    await probe.render(1);
    expect(probe.calls()).toBe(1);

    await probe.render(2);
    expect(probe.calls()).toBe(2);

    // The later request wins; the earlier one landing afterwards is ignored.
    probe.pending[1].resolve("second");
    await act(async () => {});
    probe.pending[0].resolve("first");
    await act(async () => {});
    expect(probe.latest().data).toBe("second");
  });

  it("drops a response that lands after the caller unmounted", async () => {
    const probe = mount();
    await probe.render(1);
    expect(probe.calls()).toBe(1);

    await probe.unmount();
    // Resolving after unmount must not throw or write state; if the guard were
    // gone this would warn or reject inside the effect.
    expect(() => probe.pending[0].resolve("late")).not.toThrow();
    await act(async () => {});
    expect(probe.calls()).toBe(1);
  });
});