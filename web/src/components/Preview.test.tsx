import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { SafeFrame, previewSandbox } from './Preview';
describe('artifact preview boundary',()=>{
 it('uses only allow-scripts in an opaque sandbox',()=>{render(<SafeFrame document={'<!doctype html><script>window.test=true</script>'} title="安全预览"/>);const frame=screen.getByTitle('安全预览');expect(frame).toHaveAttribute('sandbox','allow-scripts');expect(frame).not.toHaveAttribute('src');expect(frame).toHaveAttribute('referrerpolicy','no-referrer');expect(previewSandbox).not.toContain('allow-same-origin');expect(frame).toHaveAttribute('srcdoc','<!doctype html><script>window.test=true</script>')});
});
