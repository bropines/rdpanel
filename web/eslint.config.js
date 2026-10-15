import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist", "dist-mock", "node_modules", "src/api/schema.d.ts", "test-results", "playwright-report"] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ["**/*.{ts,tsx}"],
    languageOptions: { ecmaVersion: 2022, globals: globals.browser },
    plugins: { "react-hooks": reactHooks, "react-refresh": reactRefresh },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": ["warn", { allowConstantExport: true }],
      "no-restricted-syntax": [
        "error",
        {
          selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
          message: "Only the sanitized welcome markdown component may use dangerouslySetInnerHTML (DOMPurify).",
        },
      ],
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_", varsIgnorePattern: "^_" }],
    },
  },
  {
    // Pages co-locate small pure helpers (query builders, schemas) with the
    // component that uses them so they can be unit-tested; HMR granularity is
    // not worth splitting them out.
    files: ["src/components/ui/**/*.tsx", "src/pages/**/*.tsx", "src/components/*.tsx"],
    rules: { "react-refresh/only-export-components": "off" },
  },
  {
    files: ["*.js", "*.ts", "scripts/**/*.mjs"],
    languageOptions: { globals: globals.node },
  },
);
