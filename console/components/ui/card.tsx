"use client";

import * as React from "react";

export type CardProps = React.HTMLAttributes<HTMLDivElement> & {
  title?: string;
};

/** Surface container with an optional heading. */
export function Card({ title, children, className, ...props }: CardProps) {
  return (
    <div className={`console-surface ${className ?? ""}`} {...props}>
      {title ? <h3 className="console-card-title">{title}</h3> : null}
      {children}
    </div>
  );
}