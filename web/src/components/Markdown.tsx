import DOMPurify from "dompurify";
import { marked } from "marked";
import { useMemo } from "react";

import { cn } from "@/lib/utils";

/** Substitutes `{{username}}`; the value is escaped by the markdown → sanitize pipeline. */
export function substituteVars(md: string, vars: Record<string, string>): string {
  return md.replace(/\{\{\s*(\w+)\s*\}\}/g, (m, k: string) => {
    const v = vars[k];
    if (v === undefined) return m;
    // Escape markdown/HTML metacharacters so IdP-controlled names cannot inject markup.
    return v.replace(/[\\`*_{}[\]()#+\-.!<>&|~"']/g, (c) => `&#${c.charCodeAt(0)};`);
  });
}

/** Markdown → sanitized HTML (no scripts, styles, iframes, event handlers; links open safely). */
export function renderMarkdown(md: string): string {
  const html = marked.parse(md, { async: false, gfm: true, breaks: true }) as string;
  const clean = DOMPurify.sanitize(html, {
    USE_PROFILES: { html: true },
    FORBID_TAGS: ["style", "form", "input", "button", "textarea", "select", "iframe", "object", "embed"],
    FORBID_ATTR: ["style"],
  });
  return clean;
}

DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer nofollow");
  }
});

/** The only component allowed to inject HTML (SPEC §9): sanitized welcome markdown. */
export function Markdown({
  source,
  vars = {},
  className,
}: {
  source: string;
  vars?: Record<string, string>;
  className?: string;
}) {
  const html = useMemo(() => renderMarkdown(substituteVars(source, vars)), [source, vars]);
  return (
    <div
      className={cn(
        "prose-sm max-w-none text-sm leading-relaxed [&_a]:text-primary [&_a]:underline [&_code]:rounded [&_code]:bg-muted [&_code]:px-1 [&_h1]:mb-2 [&_h1]:text-lg [&_h1]:font-semibold [&_h2]:mb-2 [&_h2]:font-semibold [&_li]:ml-5 [&_ol]:list-decimal [&_p]:mb-2 [&_ul]:list-disc",
        className,
      )}
      // eslint-disable-next-line no-restricted-syntax -- sanitized by DOMPurify above
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}
