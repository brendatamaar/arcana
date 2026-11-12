import js from "@eslint/js";
import globals from "globals";
import jsxA11y from "eslint-plugin-jsx-a11y";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["node_modules", "src/api/schema.d.ts", "playwright-report", "test-results"] },
  js.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  jsxA11y.flatConfigs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
    },
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // AGENTS rule 13: nothing that turns untrusted data into markup or code.
      "no-eval": "error",
      "no-implied-eval": "error",
      "no-new-func": "error",
      "no-restricted-syntax": [
        "error",
        {
          selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
          message: "dangerouslySetInnerHTML is forbidden: render untrusted data as text.",
        },
        {
          selector: "MemberExpression[property.name='innerHTML']",
          message: "innerHTML is forbidden.",
        },
        {
          selector: "MemberExpression[property.name='outerHTML']",
          message: "outerHTML is forbidden.",
        },
        {
          selector: "CallExpression[callee.property.name='insertAdjacentHTML']",
          message: "insertAdjacentHTML is forbidden.",
        },
        {
          selector: "MemberExpression[object.name=/^(localStorage|sessionStorage)$/]",
          message: "Never store keys or tokens in web storage.",
        },
        {
          selector: "MemberExpression[object.property.name=/^(localStorage|sessionStorage)$/]",
          message: "Never store keys or tokens in web storage.",
        },
      ],
      "@typescript-eslint/restrict-template-expressions": ["error", { allowNumber: true }],
      "@typescript-eslint/no-confusing-void-expression": ["error", { ignoreArrowShorthand: true }],
      // A scrollable log must be reachable by keyboard (WCAG 2.1.1).
      "jsx-a11y/no-noninteractive-tabindex": [
        "error",
        { roles: ["log"], tags: [], allowExpressionValues: true },
      ],
    },
  },
  {
    // Tests and the e2e suite legitimately read innerHTML to prove that markup was escaped.
    files: ["src/**/*.test.{ts,tsx}", "src/test/**", "e2e/**"],
    rules: {
      "no-restricted-syntax": "off",
      "@typescript-eslint/no-non-null-assertion": "off",
      "@typescript-eslint/no-unnecessary-condition": "off",
    },
  },
  {
    files: ["**/*.{js,mjs}"],
    ...tseslint.configs.disableTypeChecked,
    languageOptions: { globals: globals.node },
  },
);
