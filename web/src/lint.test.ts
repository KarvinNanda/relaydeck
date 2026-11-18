// @vitest-environment node
import { ESLint } from 'eslint'
import { describe, expect, it } from 'vitest'

// Agent and message text must never become live HTML. The ESLint config
// makes v-html an error; this test proves the rule is really on.
describe('eslint config', () => {
  it('fails on v-html', async () => {
    const eslint = new ESLint()
    const [res] = await eslint.lintText('<template><div v-html="body"></div></template>\n', { filePath: 'src/components/X.vue' })
    expect(res.messages.map((m) => [m.ruleId, m.severity])).toContainEqual(['vue/no-v-html', 2])
  })

  it('accepts text interpolation', async () => {
    const eslint = new ESLint()
    const [res] = await eslint.lintText('<template><div>{{ body }}</div></template>\n', { filePath: 'src/components/X.vue' })
    expect(res.errorCount).toBe(0)
  })
})
