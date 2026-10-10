import React from 'react';
import { Link } from 'react-router';
import { Icon, Skeleton, useNavigation } from '@oneclub/shell';
import { ProgramSwitch } from './sport';
import { Head } from './ui';

// Book (Plan Your Visit): the booking journeys of the club, grouped Golf ·
// Resort · Events · More. The tiles are the Book menu of the server
// navigation, so a disabled module hides its tiles.

const TILES: Record<string, [string, string, string]> = {
  'book-tee-time': ['golf', 'sports_golf', 'Saturday morning round? Pick a tee time and the players'],
  'book-driving-range': ['golf', 'golf_course', 'A bay and a time, or just drop by; balls at the counter'],
  tournaments: ['golf', 'emoji_events', 'Register for club tournaments'],
  'course-guide': ['golf', 'map', 'Hole by hole, tees and course handicap'],
  'book-court': ['sport', 'sports_tennis', 'Tennis, futsal, basket & volley: pick a free hour and pay'],
  'sport-classes': ['sport', 'school', 'Swimming, tennis and aikido classes; book sessions'],
  'sport-access': ['sport', 'pool', 'Pool & gym: your member card, guest tickets'],
  'book-bungalow': ['resort', 'cottage', 'Stay the night: choose dates and a bungalow'],
  'book-meeting-room': ['resort', 'meeting_room', 'Rooms for meetings and private events'],
  'upcoming-events': ['events', 'celebration', 'Golf days, dinners and club events'],
  'sport-club': ['more', 'sports_tennis', 'Courts and classes'],
  'order-food': ['more', 'restaurant', 'Pre-order or on-course delivery'],
  packages: ['more', 'card_travel', 'Golf & stay packages'],
  offers: ['more', 'local_offer', 'Offers for members'],
};
const GROUPS: [string, string][] = [['golf', 'Golf'], ['sport', 'Sport Club'], ['resort', 'Resort'], ['events', 'Events'], ['more', 'More']];

export function BookHub() {
  const nav = useNavigation('member');
  const items = nav.data?.items.find((i) => i.key === 'book')?.children ?? [];
  return (
    <div className="mj-page">
      <Head title="Plan Your Visit" help="Book a tee time or a court, a bungalow, a meeting room or an event." />
      <ProgramSwitch />
      {nav.isLoading && <Skeleton rows={4} />}
      {GROUPS.map(([g, label]) => {
        const tiles = items.filter((i) => (TILES[i.key]?.[0] ?? 'more') === g);
        if (!tiles.length) return null;
        return (
          <section key={g}>
            <h2 className="mj-section-title">{label}</h2>
            <div className="mj-quick">
              {tiles.map((t) => (
                <Link key={t.key} to={t.path}>
                  <span className="mj-icon"><Icon name={TILES[t.key]?.[1] ?? 'event'} size={22} /></span>
                  <span>{t.label}<small>{TILES[t.key]?.[2]}</small></span>
                </Link>
              ))}
            </div>
          </section>
        );
      })}
    </div>
  );
}
