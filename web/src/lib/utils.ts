import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/** Merges Tailwind class names, letting later utilities win (shadcn/ui helper). */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
