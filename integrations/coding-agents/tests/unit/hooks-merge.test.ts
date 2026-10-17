import { describe, expect, it } from "vitest";
import { HOOKS_CONTAINER_KEY, MATCHER_GROUP_KEY } from "../../src/hosts/hook-shape";
import {
  mergeValueIntoContainer,
  removeOwnValuesFromContainer,
  deleteAtPath,
  documentIsEmpty,
  pruneEmptyContainers,
  type JsonDocument,
} from "../../src/write/json-merge";
import { OWNERSHIP_MARKER } from "../../src/write/marker";

/**
 * The nested-value merge and removal primitives: the package's own value is
 * replaced in place, a foreign value on the same member is never overwritten,
 * and a removal strips only what carries the ownership marker before pruning
 * the containers it emptied.
 */

const OUR_VALUE = { [MATCHER_GROUP_KEY]: [{ type: "command", command: `/x/${OWNERSHIP_MARKER}/stop.mjs`, timeout: 30 }] };
const FOREIGN_VALUE = { [MATCHER_GROUP_KEY]: [{ type: "command", command: "echo foreign", timeout: 5 }] };
const FOREIGN_ENTRY = { command: "echo foreign" };

describe("mergeValueIntoContainer", () => {
  it("sets the member on a fresh document", () => {
    const document: JsonDocument = {};
    mergeValueIntoContainer(document, [HOOKS_CONTAINER_KEY, "Stop"], OUR_VALUE);
    expect(document).toEqual({ [HOOKS_CONTAINER_KEY]: { Stop: OUR_VALUE } });
  });

  it("replaces a key already holding one of ours, so a re-run stacks nothing", () => {
    const document: JsonDocument = { [HOOKS_CONTAINER_KEY]: { Stop: OUR_VALUE } };
    mergeValueIntoContainer(document, [HOOKS_CONTAINER_KEY, "Stop"], OUR_VALUE);
    const hooks = document[HOOKS_CONTAINER_KEY] as JsonDocument;
    expect(hooks["Stop"]).toEqual(OUR_VALUE);
  });

  it("leaves a foreign value on the same member untouched", () => {
    const document: JsonDocument = { [HOOKS_CONTAINER_KEY]: { Stop: FOREIGN_VALUE } };
    mergeValueIntoContainer(document, [HOOKS_CONTAINER_KEY, "Stop"], OUR_VALUE);
    const hooks = document[HOOKS_CONTAINER_KEY] as JsonDocument;
    expect(hooks["Stop"]).toEqual(FOREIGN_VALUE);
  });

  it("keeps a foreign list's entries and appends ours after them", () => {
    const document: JsonDocument = { [HOOKS_CONTAINER_KEY]: { Stop: [FOREIGN_ENTRY] } };
    mergeValueIntoContainer(document, [HOOKS_CONTAINER_KEY, "Stop"], OUR_VALUE);
    const hooks = document[HOOKS_CONTAINER_KEY] as JsonDocument;
    expect(hooks["Stop"]).toEqual([FOREIGN_ENTRY, OUR_VALUE]);
  });

  it("appends at most once into a foreign list on a re-run", () => {
    const document: JsonDocument = { [HOOKS_CONTAINER_KEY]: { Stop: [FOREIGN_ENTRY] } };
    mergeValueIntoContainer(document, [HOOKS_CONTAINER_KEY, "Stop"], OUR_VALUE);
    mergeValueIntoContainer(document, [HOOKS_CONTAINER_KEY, "Stop"], OUR_VALUE);
    const hooks = document[HOOKS_CONTAINER_KEY] as JsonDocument;
    expect(hooks["Stop"]).toEqual([FOREIGN_ENTRY, OUR_VALUE]);
  });

  it("preserves unrelated members and creates missing nesting", () => {
    const document: JsonDocument = { model: "opus" };
    mergeValueIntoContainer(document, [HOOKS_CONTAINER_KEY, "Stop"], OUR_VALUE);
    expect(document["model"]).toBe("opus");
  });
});

describe("removeOwnValuesFromContainer", () => {
  it("removes the value when it carries the ownership marker", () => {
    const document: JsonDocument = { [HOOKS_CONTAINER_KEY]: { Stop: OUR_VALUE } };
    expect(removeOwnValuesFromContainer(document, [HOOKS_CONTAINER_KEY, "Stop"])).toBe(1);
    expect((document[HOOKS_CONTAINER_KEY] as JsonDocument)["Stop"]).toBeUndefined();
  });

  it("leaves a foreign value in place", () => {
    const document: JsonDocument = { [HOOKS_CONTAINER_KEY]: { Stop: FOREIGN_VALUE } };
    expect(removeOwnValuesFromContainer(document, [HOOKS_CONTAINER_KEY, "Stop"])).toBe(0);
    expect((document[HOOKS_CONTAINER_KEY] as JsonDocument)["Stop"]).toEqual(FOREIGN_VALUE);
  });

  it("strips only our entry from a foreign list, restoring the host's own", () => {
    const document: JsonDocument = { [HOOKS_CONTAINER_KEY]: { Stop: [FOREIGN_ENTRY, OUR_VALUE] } };
    expect(removeOwnValuesFromContainer(document, [HOOKS_CONTAINER_KEY, "Stop"])).toBe(1);
    expect((document[HOOKS_CONTAINER_KEY] as JsonDocument)["Stop"]).toEqual([FOREIGN_ENTRY]);
  });

  it("deletes the member when our entry was the only element of the list", () => {
    const document: JsonDocument = { [HOOKS_CONTAINER_KEY]: { Stop: [OUR_VALUE] } };
    expect(removeOwnValuesFromContainer(document, [HOOKS_CONTAINER_KEY, "Stop"])).toBe(1);
    expect((document[HOOKS_CONTAINER_KEY] as JsonDocument)["Stop"]).toBeUndefined();
  });

  it("reports nothing removed when the path is absent or the parent is not an object", () => {
    expect(removeOwnValuesFromContainer({}, [HOOKS_CONTAINER_KEY, "Stop"])).toBe(0);
    expect(removeOwnValuesFromContainer({ [HOOKS_CONTAINER_KEY]: "scalar" }, [HOOKS_CONTAINER_KEY, "Stop"])).toBe(0);
  });
});

describe("pruning the containers a removal emptied", () => {
  it("drops a key whose value is an empty array or an empty object", () => {
    const document: JsonDocument = { hooks: {}, other: [], keep: { a: 1 } };
    pruneEmptyContainers(document);
    expect(document).toEqual({ keep: { a: 1 } });
  });

  it("climbs out of an emptied nested container but keeps a non-empty parent", () => {
    const document: JsonDocument = { hooks: { Stop: OUR_VALUE, SessionStart: FOREIGN_VALUE } };
    deleteAtPath(document, [HOOKS_CONTAINER_KEY, "Stop"]);
    expect(document).toEqual({ [HOOKS_CONTAINER_KEY]: { SessionStart: FOREIGN_VALUE } });
  });

  it("reports a document empty only when it holds nothing", () => {
    expect(documentIsEmpty({})).toBe(true);
    expect(documentIsEmpty({ hooks: {} })).toBe(false);
  });
});