"use client";

import * as Tabs from "@radix-ui/react-tabs";
import type { ReactNode } from "react";

export type TabItem = {
  value: string;
  label: string;
  content: ReactNode;
};

/** Tabbed panel built on Radix, used to switch view sub-tabs. */
export function TabPanel({
  items,
  value,
  onValueChange,
}: {
  items: TabItem[];
  value: string;
  onValueChange: (value: string) => void;
}) {
  return (
    <Tabs.Root value={value} onValueChange={onValueChange}>
      <Tabs.List aria-label="subtabs">
        {items.map((item) => (
          <Tabs.Trigger key={item.value} value={item.value}>
            {item.label}
          </Tabs.Trigger>
        ))}
      </Tabs.List>
      {items.map((item) => (
        <Tabs.Content key={item.value} value={item.value}>
          {item.content}
        </Tabs.Content>
      ))}
    </Tabs.Root>
  );
}