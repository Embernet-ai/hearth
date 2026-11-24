import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

describe('the numbers on the dashboard', () => {
  test('every tile says its period and links to the records behind it', () => {
    const d = draw('dashboard', fixture('dashboard-numbers'));
    const tiles = [...d.querySelectorAll('.numbers a.stat')];
    expect(tiles.length).toBe(4);
    for (const a of tiles) {
      expect(a.getAttribute('href'), a.textContent ?? '').toMatch(/^\//);
      expect(a.querySelector('.period')?.textContent?.trim(), 'a tile with no period').toBeTruthy();
    }
    const week = tiles[0];
    expect(week.textContent).toContain('12h 30m');
    expect(week.textContent).toContain('of 40h');
    expect(week.querySelector('.meter')).not.toBeNull();
    // the note about the standard week is said once, under the group's title, not inside a tile
    expect(d.querySelector('.numbers .groupnote')?.textContent).toContain('standard 40 hour week');
    expect(week.textContent).not.toContain('standard 40 hour week');
    expect(tiles[1].classList.contains('bad')).toBe(true);
    expect(d.querySelector('.numbers h2')?.textContent).toBe('My numbers');
  });

  test('shows the last eight weeks, the pipeline by stage, and what closed', () => {
    const d = draw('dashboard', fixture('dashboard-numbers'));
    expect(d.querySelectorAll('.weeks li').length).toBe(2);
    expect(d.querySelector('.weeks li.current a')?.getAttribute('href')).toBe('/time?from=2026-11-16');
    expect(d.querySelector('.stagebars a')?.getAttribute('href')).toContain('stage=proposal');
    expect(d.querySelector('.recent a')?.getAttribute('href')).toBe('/deals/d1');
  });

  test('somebody who may see colleagues gets a picker, and everybody else gets none', () => {
    const own = draw('dashboard', fixture('dashboard-numbers'));
    expect(own.querySelector('select[name=person]')).toBeNull();
    const d = draw('dashboard', fixture('dashboard-numbers-picker'));
    expect(d.querySelector('select[name=person]')).not.toBeNull();
    expect(d.querySelector('option[value=p2][selected]')).not.toBeNull();
    expect(d.querySelector('.numbers h2')?.textContent).toBe('Numbers for Aria');
  });
});

describe('the time page for a colleague', () => {
  test('is read only, says whose it is, and keeps the month and the person in its links', () => {
    const d = draw('time', fixture('time-colleague'));
    const t = text(d);
    expect(t).toContain("Aria's time");
    expect(t).toContain('wiring');
    expect(t).toContain('this month');
    expect(d.querySelector('form[action="/time"]'), 'the form that records your own hours').toBeNull();
    expect(d.querySelector('form[action="/time/start"]')).toBeNull();
    expect(d.querySelector('form[action^="/time/entries/"]'), 'a way to correct or remove their entries').toBeNull();
    const prev = [...d.querySelectorAll('a.chip')].find((a) => a.textContent === 'Previous');
    expect(prev?.getAttribute('href')).toContain('&span=month&person=p2');
  });

  test('your own is unchanged', () => {
    const d = draw('time', fixture('time-own'));
    expect(text(d)).toContain('My time');
    expect(d.querySelector('form[action="/time"]')).not.toBeNull();
    expect(d.querySelector('form[action^="/time/entries/"]')).not.toBeNull();
  });
});
