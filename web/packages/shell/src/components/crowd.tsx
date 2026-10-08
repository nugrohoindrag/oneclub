import React from 'react';

// How busy a tee time or driving range time is. A busy time never refuses
// a booking (first come first served); it is shown red so the player knows
// to expect a queue, a quiet time green.

export type Crowd = 'peak' | 'quiet';

/** Classes for a slot button coloured by crowd (red peak, green quiet). */
export const crowdClass = (crowd?: string | null) => (crowd === 'peak' ? 'oc-crowd-peak' : 'oc-crowd-quiet');

/** Peak / Quiet label with a red or green dot. */
export function CrowdLabel({ crowd, peak = 'Peak', quiet = 'Quiet' }: { crowd?: string | null; peak?: string; quiet?: string }) {
  return <span className="oc-crowd" data-crowd={crowd === 'peak' ? 'peak' : 'quiet'}>{crowd === 'peak' ? peak : quiet}</span>;
}
