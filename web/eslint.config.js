import pluginVue from 'eslint-plugin-vue'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['node_modules/**', '../internal/**'] },
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/essential'],
  {
    files: ['**/*.vue'],
    languageOptions: { parserOptions: { parser: tseslint.parser } },
  },
  {
    rules: {
      // Agent and message text is rendered only through text interpolation.
      // v-html would turn text written by an agent into live HTML.
      'vue/no-v-html': 'error',
      // The brief fixes single-word names (Sidebar.vue); there are no HTML
      // elements with these names to clash with.
      'vue/multi-word-component-names': 'off',
      // `const { type, ...rest } = frame` drops a field on purpose.
      '@typescript-eslint/no-unused-vars': ['error', { ignoreRestSiblings: true }],
    },
  },
)
