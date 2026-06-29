import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import jsxA11y from 'eslint-plugin-jsx-a11y'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
      jsxA11y.flatConfigs.recommended,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    rules: {
      // eslint-plugin-react-hooks 7.1 promoted this to an error in the
      // recommended preset. Our remaining hits are idiomatic fetch-on-mount /
      // prop-sync effects (synchronous setLoading(true) before an await), not
      // cascading-render bugs — keep it visible as a warning to address
      // incrementally rather than blocking lint or forcing risky rewrites.
      'react-hooks/set-state-in-effect': 'warn',
    },
  },
  {
    files: ['src/contexts/**/*.tsx'],
    rules: {
      // Context modules intentionally export Provider + hook together; an HMR
      // update of a context invalidates its consumers, which is acceptable.
      'react-refresh/only-export-components': 'off',
    },
  },
])
