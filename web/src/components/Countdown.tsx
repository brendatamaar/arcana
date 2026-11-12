import { useEffect, useState } from "react";
import { formatDuration } from "../lib/format";

/** The current time, re-read every `interval` ms. */
export function useNow(interval = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), interval);
    return () => clearInterval(id);
  }, [interval]);
  return now;
}

export function Countdown({ seconds, label }: { seconds: number | null; label: string }) {
  return (
    <span>
      {label}: {seconds === null ? "disabled" : seconds <= 0 ? "elapsed" : formatDuration(seconds)}
    </span>
  );
}
