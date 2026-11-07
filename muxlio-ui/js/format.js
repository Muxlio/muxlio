// Shared display units. Numeric durations are milliseconds unless specified.
const MuxlioFormat=Object.freeze({
 tokens(value){
  if(typeof value!=='number'||!Number.isFinite(value)||value<0)return 'Unavailable';
  if(value>=999950000)return `${(value/1e9).toFixed(1)}B`;
  if(value>=999950)return `${(value/1e6).toFixed(1)}M`;
  if(value>=1000)return `${(value/1000).toFixed(1)}K`;
  return String(value);
 },
 duration(value,unit='milliseconds'){
  const scales={milliseconds:1,seconds:1000,minutes:60000,hours:3600000,days:86400000};
  if(typeof value!=='number'||!Number.isFinite(value)||!Object.hasOwn(scales,unit))return 'Unavailable';
  const ms=Math.max(0,value)*scales[unit];
  if(!Number.isFinite(ms))return 'Unavailable';
  if(ms<1000)return `${Math.round(ms)} ms`;
  if(ms<60000)return `${(ms/1000).toFixed(1)} s`;
  if(ms<3600000)return `${Math.floor(ms/60000)}m ${Math.floor(ms%60000/1000)}s`;
  if(ms<86400000)return `${Math.floor(ms/3600000)}h ${Math.floor(ms%3600000/60000)}m`;
  return `${Math.floor(ms/86400000)}d ${Math.floor(ms%86400000/3600000)}h`;
 },
 timestamp(value,timeOnly=false){
  if(value==null||value===''||String(value).startsWith('0001-01-01T'))return 'Unavailable';
  const stamp=new Date(value);
  if(!Number.isFinite(stamp.getTime()))return 'Unavailable';
  return timeOnly?stamp.toLocaleTimeString(undefined,{hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}):stamp.toLocaleString();
 },
 elapsed(start,end){
  if(start==null||end==null||start===''||end==='')return 'Unavailable';
  return MuxlioFormat.duration(new Date(end)-new Date(start));
 }
});

// Existing views share these names rather than maintain separate formatters.
function fmtTokens(value){return MuxlioFormat.tokens(value)}
function fmtTok(value){return MuxlioFormat.tokens(value)}
function fmtDuration(value){return MuxlioFormat.duration(value)}
function duration(start,end){return MuxlioFormat.elapsed(start,end)}
function formatElapsed(start,end){return MuxlioFormat.elapsed(start,end)}
function date(value){return MuxlioFormat.timestamp(value)}
