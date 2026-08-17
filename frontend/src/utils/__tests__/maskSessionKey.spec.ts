import { describe, expect, it } from 'vitest'
import { maskSessionKey } from '@/utils/maskSessionKey'

describe('maskSessionKey', () => {
  it('shows the first four and last four characters for normal session keys', () => {
    expect(maskSessionKey('sk-ant-sid01-example-12345678')).toBe('sk-a…5678')
  })

  it('trims surrounding whitespace before building the preview', () => {
    expect(maskSessionKey('  1234567890  ')).toBe('1234…7890')
  })

  it('does not reveal a complete short secret', () => {
    expect(maskSessionKey('12345678')).toBe('12…78')
    expect(maskSessionKey('1234')).toBe('••••')
  })

  it('returns an empty preview for blank input', () => {
    expect(maskSessionKey('   ')).toBe('')
  })
})
