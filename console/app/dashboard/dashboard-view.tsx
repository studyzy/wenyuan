"use client";

import { useEffect } from "react";
import { useBankSelection } from "../providers/bank-provider";

/**
 * Dashboard: hosts the all-banks overview and clears the current bank selection
 * so header actions are never offered for a bank that is not on screen.
 */
export function DashboardView({ banks }: { banks: Array<{ bank_id: string; name?: string }> }) {
  const { clearBank } = useBankSelection();

  useEffect(() => {
    clearBank();
  }, [clearBank]);

  return (
    <section>
      <table>
        <thead>
          <tr>
            <th>bank_id</th>
            <th>name</th>
          </tr>
        </thead>
        <tbody>
          {banks.map((bank) => (
            <tr key={bank.bank_id}>
              <td>{bank.bank_id}</td>
              <td>{bank.name ?? ""}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}