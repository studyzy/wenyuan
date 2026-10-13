"use client";

import { createContext, useContext, useMemo, useState, type ReactNode } from "react";

/** Shared notification surface consumed by the shell. */
export type NotificationLevel = "info" | "success" | "error";

export type Notification = {
  id: string;
  level: NotificationLevel;
  message: string;
};

type NotificationContextValue = {
  notifications: Notification[];
  notify: (level: NotificationLevel, message: string) => void;
  dismiss: (id: string) => void;
};

const NotificationContext = createContext<NotificationContextValue | null>(null);

export function NotificationProvider({ children }: { children: ReactNode }) {
  const [notifications, setNotifications] = useState<Notification[]>([]);

  const value = useMemo<NotificationContextValue>(
    () => ({
      notifications,
      notify: (level, message) =>
        setNotifications((current) => [
          ...current,
          { id: `${Date.now()}-${current.length}`, level, message },
        ]),
      dismiss: (id) =>
        setNotifications((current) => current.filter((entry) => entry.id !== id)),
    }),
    [notifications],
  );

  return (
    <NotificationContext.Provider value={value}>
      {children}
      <div className="console-notifications" role="status" aria-live="polite">
        {notifications.map((entry) => (
          <div key={entry.id} className={`console-notification console-notification-${entry.level}`}>
            {entry.message}
          </div>
        ))}
      </div>
    </NotificationContext.Provider>
  );
}

export function useNotifications(): NotificationContextValue {
  const context = useContext(NotificationContext);
  if (!context) {
    throw new Error("useNotifications must be used inside NotificationProvider");
  }
  return context;
}