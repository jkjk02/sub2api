import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { describe, expect, it, vi } from 'vitest'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.accounts.oauth.authorizingKey' && params) {
          return `Authorizing ${params.current}/${params.total} · ${params.key}`
        }
        return key
      }
    })
  }
})

vi.mock('@/api/admin', () => ({
  adminAPI: {
    grok: {
      getCapabilities: vi.fn().mockResolvedValue({ password_auth_enabled: false })
    }
  }
}))

import OAuthAuthorizationFlow from '../OAuthAuthorizationFlow.vue'

const mountCookieFlow = () =>
  mount(OAuthAuthorizationFlow, {
    props: {
      addMethod: 'oauth',
      initialInputMethod: 'cookie',
      showManualOption: false,
      showCookieOption: true,
      allowMultiple: true,
      platform: 'anthropic'
    },
    global: {
      plugins: [createPinia()],
      stubs: {
        Icon: true
      }
    }
  })

describe('OAuthAuthorizationFlow cookie key preview', () => {
  it('shows a masked identifier for every parsed session key', async () => {
    const wrapper = mountCookieFlow()
    const input = '1234567890\nsk-ant-sid01-example-87654321'

    await wrapper.get('textarea').setValue(input)

    expect(wrapper.findAll('code').map((item) => item.text())).toEqual([
      '1234…7890',
      'sk-a…4321'
    ])
  })

  it('uses the parent supplied masked key progress while authorizing', async () => {
    const wrapper = mountCookieFlow()

    await wrapper.get('textarea').setValue('1234567890')
    await wrapper.setProps({
      loading: true,
      cookieAuthProgress: 'Authorizing 1/2 · 1234…7890'
    })

    expect(wrapper.get('button.btn-primary').text()).toContain('Authorizing 1/2 · 1234…7890')
  })

  it('keeps emitting the original session key input for authentication', async () => {
    const wrapper = mountCookieFlow()
    const input = '1234567890\nabcdefghij'

    await wrapper.get('textarea').setValue(input)
    await wrapper.get('button.btn-primary').trigger('click')

    expect(wrapper.emitted('cookie-auth')).toEqual([[input]])
  })
})
